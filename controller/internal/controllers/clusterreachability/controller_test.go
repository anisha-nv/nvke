// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clusterreachability

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dsx-ai-factory/nke/controller/internal/controllers/dbreconcile"
	"github.com/dsx-ai-factory/nke/controller/internal/controllers/dbreconcile/dbwrapper"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
)

type probeFunc func(context.Context, db.Cluster) (Observation, error)

func (f probeFunc) Probe(ctx context.Context, cluster db.Cluster) (Observation, error) {
	return f(ctx, cluster)
}

type observationDB struct {
	db.DB
	mu                sync.Mutex
	rows              map[uuid.UUID]db.Cluster
	readErr, writeErr error
	writes            int
	lockedReads       int
}

func copyCluster(row db.Cluster) db.Cluster {
	if row.Conditions.APIServerReachable != nil {
		value := *row.Conditions.APIServerReachable
		row.Conditions.APIServerReachable = &value
	}
	return row
}

func (d *observationDB) GetCluster(_ context.Context, id uuid.UUID) (db.Cluster, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.get(id)
}

func (d *observationDB) get(id uuid.UUID) (db.Cluster, error) {
	if d.readErr != nil {
		return db.Cluster{}, d.readErr
	}
	row, ok := d.rows[id]
	if !ok {
		return db.Cluster{}, db.ErrNotFound
	}
	return copyCluster(row), nil
}

func (d *observationDB) RunInTx(ctx context.Context, fn func(context.Context, db.Tx) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fn(ctx, &observationTx{database: d})
}

type observationTx struct {
	db.Tx
	database *observationDB
}

func (tx *observationTx) GetCluster(_ context.Context, id uuid.UUID) (db.Cluster, error) {
	return tx.database.get(id)
}

func (tx *observationTx) GetClusterForUpdate(_ context.Context, id uuid.UUID) (db.Cluster, error) {
	tx.database.lockedReads++
	return tx.database.get(id)
}

func (tx *observationTx) UpdateCluster(_ context.Context, row *db.Cluster) (db.Cluster, error) {
	if tx.database.writeErr != nil {
		return db.Cluster{}, tx.database.writeErr
	}
	tx.database.writes++
	tx.database.rows[row.ID] = copyCluster(*row)
	return copyCluster(*row), nil
}

func testObserver(rows []db.Cluster, prober Prober) (*Controller, *observationDB) {
	database := &observationDB{rows: make(map[uuid.UUID]db.Cluster)}
	for _, row := range rows {
		database.rows[row.ID] = row
	}
	logger := slog.New(slog.DiscardHandler)
	return NewController(logger, dbwrapper.New(database, logger), prober), database
}

func TestReachabilityScheduling(t *testing.T) {
	row := db.Cluster{ID: uuid.New()}
	observation := Observation{Status: db.ConditionStatusTrue, Message: "Kubernetes API is reachable"}
	calls := 0
	controller, database := testObserver([]db.Cluster{row}, probeFunc(func(context.Context, db.Cluster) (Observation, error) {
		calls++
		return observation, nil
	}))
	now := time.Now()
	controller.now = func() time.Time { return now }
	key := dbreconcile.ClusterKey(row.ID)
	result, err := controller.Reconcile(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, probeInterval, result.RequeueAfter)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, database.writes)
	transition := database.rows[row.ID].Conditions.APIServerReachable.LastTransitionTime
	// A self-generated source event cannot trigger another tenant request.
	now = now.Add(30 * time.Second)
	result, err = controller.Reconcile(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, 90*time.Second, result.RequeueAfter)
	require.Equal(t, 1, calls)
	now = now.Add(90 * time.Second)
	_, err = controller.Reconcile(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, 1, database.writes, "unchanged observations must not heartbeat the row")
	require.True(t, transition.Equal(database.rows[row.ID].Conditions.APIServerReachable.LastTransitionTime))

	observation.Message = "Another safe message"
	now = now.Add(probeInterval)
	_, err = controller.Reconcile(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, 2, database.writes)
	require.True(t, transition.Equal(database.rows[row.ID].Conditions.APIServerReachable.LastTransitionTime))
	// Startup always probes even if the stored transition time is recent.
	restarted := NewController(slog.New(slog.DiscardHandler), controller.db, controller.prober)
	_, err = restarted.Reconcile(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, 4, calls)
}

func TestReachabilityPreservesObservationsOnErrors(t *testing.T) {
	for _, status := range []db.ConditionStatus{db.ConditionStatusTrue, db.ConditionStatusFalse} {
		for _, failure := range []string{"management", "database read", "database write", "shutdown"} {
			t.Run(string(status)+"/"+failure, func(t *testing.T) {
				before := db.Condition{Status: status, Message: "Previous observation", LastTransitionTime: time.Now().Add(-time.Hour)}
				row := db.Cluster{ID: uuid.New(), Conditions: db.ClusterConditions{APIServerReachable: &before}}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				calls := 0
				controller, database := testObserver([]db.Cluster{row}, probeFunc(func(context.Context, db.Cluster) (Observation, error) {
					calls++
					if failure == "management" {
						return Observation{}, errors.New("SENTINEL-reader-error")
					}
					if failure == "shutdown" {
						cancel()
					}
					return Observation{Status: db.ConditionStatusUnknown, Message: "Tenant access is not available"}, nil
				}))
				if failure == "database read" {
					database.readErr = errors.New("SENTINEL-read")
				}
				if failure == "database write" {
					database.writeErr = errors.New("SENTINEL-write")
				}
				_, err := controller.Reconcile(ctx, dbreconcile.ClusterKey(row.ID))
				require.Error(t, err)
				require.NotContains(t, err.Error(), "SENTINEL")
				require.True(t, database.rows[row.ID].Conditions.APIServerReachable.Equal(before))
				require.Empty(t, controller.nextDue)
				if failure == "database write" {
					database.writeErr = nil
					_, err = controller.Reconcile(ctx, dbreconcile.ClusterKey(row.ID))
					require.NoError(t, err)
					require.Equal(t, 2, calls, "a failed persistence attempt must not be hidden by the due guard")
					require.Equal(t, db.ConditionStatusUnknown, database.rows[row.ID].Conditions.APIServerReachable.Status)
				}
			})
		}
	}
}

func TestReachabilityDeletionAndLatestRowMerge(t *testing.T) {
	for _, change := range []string{"missing before", "deleting before", "missing during", "deleting during", "lifecycle during"} {
		t.Run(change, func(t *testing.T) {
			row := db.Cluster{ID: uuid.New()}
			var database *observationDB
			calls := 0
			controller, store := testObserver([]db.Cluster{row}, probeFunc(func(ctx context.Context, _ db.Cluster) (Observation, error) {
				calls++
				// Taking the DB mutex here would deadlock if the probe held a row
				// transaction. Mutations model another writer during network I/O.
				database.mu.Lock()
				switch change {
				case "missing during":
					delete(database.rows, row.ID)
				case "deleting during":
					row.Conditions.Deleting = db.NewCondition(true, "Deleting")
					database.rows[row.ID] = row
				case "lifecycle during":
					row.Conditions.Provisioned = db.NewCondition(true, "Provisioned")
					database.rows[row.ID] = row
				}
				database.mu.Unlock()
				return Observation{Status: db.ConditionStatusTrue, Message: "Kubernetes API is reachable"}, ctx.Err()
			}))
			database = store
			if change == "missing before" {
				delete(database.rows, row.ID)
			}
			if change == "deleting before" {
				row.Conditions.Deleting = db.NewCondition(true, "Deleting")
				database.rows[row.ID] = row
			}
			controller.nextDue[row.ID] = time.Now().Add(-time.Minute)
			_, err := controller.Reconcile(t.Context(), dbreconcile.ClusterKey(row.ID))
			require.NoError(t, err)
			if change == "lifecycle during" {
				require.Equal(t, 1, database.writes)
				require.Equal(t, 1, database.lockedReads)
				require.Equal(t, db.ConditionStatusTrue, database.rows[row.ID].Conditions.Provisioned.Status)
				require.Equal(t, db.ConditionStatusTrue, database.rows[row.ID].Conditions.APIServerReachable.Status)
			} else {
				require.Zero(t, database.writes)
				require.Empty(t, controller.nextDue)
			}
			if change == "missing before" || change == "deleting before" {
				require.Zero(t, calls)
			}
		})
	}
}

func TestReachabilityIndependentClusterProgress(t *testing.T) {
	first, second := db.Cluster{ID: uuid.New()}, db.Cluster{ID: uuid.New()}
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	controller, _ := testObserver([]db.Cluster{first, second}, probeFunc(func(ctx context.Context, row db.Cluster) (Observation, error) {
		calls.Add(1)
		if row.ID == first.ID {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Observation{}, ctx.Err()
			}
		}
		return Observation{Status: db.ConditionStatusTrue, Message: "Kubernetes API is reachable"}, nil
	}))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := controller.Reconcile(ctx, dbreconcile.ClusterKey(first.ID)); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err := controller.Reconcile(ctx, dbreconcile.ClusterKey(second.ID))
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestReachabilityScheduledSecretRotation(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer second.Close()
	row := testCluster()
	secret := accessSecret(t, first.URL, "example-token")
	reader := managementClient(t, secret)
	controller, database := testObserver([]db.Cluster{row}, NewProber(reader))
	now := time.Now()
	controller.now = func() time.Time { return now }
	key := dbreconcile.ClusterKey(row.ID)
	_, err := controller.Reconcile(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, db.ConditionStatusTrue, database.rows[row.ID].Conditions.APIServerReachable.Status)
	require.NoError(t, reader.Get(t.Context(), client.ObjectKeyFromObject(secret), secret))
	secret.Data = accessSecret(t, second.URL, "example-rotated-token").Data
	require.NoError(t, reader.Update(t.Context(), secret))
	_, err = controller.Reconcile(t.Context(), key)
	require.NoError(t, err)
	require.Zero(t, secondCalls.Load())
	now = now.Add(probeInterval)
	_, err = controller.Reconcile(t.Context(), key)
	require.NoError(t, err)
	require.EqualValues(t, 1, firstCalls.Load())
	require.EqualValues(t, 1, secondCalls.Load())
	require.Equal(t, db.ConditionStatusFalse, database.rows[row.ID].Conditions.APIServerReachable.Status)
}
