// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package workflows

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nke/controller/internal/db"
)

// These tests replay synthetic event histories against the current workflow
// code, the same determinism check the Temporal worker performs when it picks
// up a workflow that was started by an older deployment. Each fixture pins the
// prefix of activities a running execution has in its history; if a change to
// a workflow function reorders, renames, or removes one of these scheduled
// activities without a workflow.GetVersion gate, replay fails here the same
// way it would panic in production ("history event is ActivityTaskScheduled
// ... replay command is ScheduleActivityTask ...").
//
// When one of these tests fails, the fix is normally to guard the change with
// workflow.GetVersion so in-flight executions keep replaying down the old
// path. Only update the fixture itself if every execution recorded with the
// old shape is known to be finished or has been reset.

// replayActivityStep is one sequentially executed activity in a synthetic
// history: the workflow schedules it, and (unless it is the last step, where
// the history ends while the activity is still pending) it completes with
// result.
type replayActivityStep struct {
	activityType string
	result       any
}

// sequentialActivityHistory builds the event history a Temporal server would
// record for a workflow that executes the given activities one at a time. The
// history ends with the final step's ActivityTaskScheduled event, i.e. an
// execution still in flight — exactly the state that must survive a deploy.
func sequentialActivityHistory(t *testing.T, workflowType string, input any, steps []replayActivityStep) *historypb.History {
	t.Helper()
	require.NotEmpty(t, steps)

	dc := converter.GetDefaultDataConverter()
	taskQueue := &taskqueuepb.TaskQueue{Name: "test", Kind: enumspb.TASK_QUEUE_KIND_NORMAL}

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var events []*historypb.HistoryEvent
	addEvent := func(eventType enumspb.EventType, attrs any) int64 {
		id := int64(len(events) + 1)
		event := &historypb.HistoryEvent{
			EventId:   id,
			EventTime: timestamppb.New(baseTime.Add(time.Duration(id) * time.Second)),
			EventType: eventType,
		}
		switch a := attrs.(type) {
		case *historypb.WorkflowExecutionStartedEventAttributes:
			event.Attributes = &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: a}
		case *historypb.WorkflowTaskScheduledEventAttributes:
			event.Attributes = &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{WorkflowTaskScheduledEventAttributes: a}
		case *historypb.WorkflowTaskStartedEventAttributes:
			event.Attributes = &historypb.HistoryEvent_WorkflowTaskStartedEventAttributes{WorkflowTaskStartedEventAttributes: a}
		case *historypb.WorkflowTaskCompletedEventAttributes:
			event.Attributes = &historypb.HistoryEvent_WorkflowTaskCompletedEventAttributes{WorkflowTaskCompletedEventAttributes: a}
		case *historypb.ActivityTaskScheduledEventAttributes:
			event.Attributes = &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{ActivityTaskScheduledEventAttributes: a}
		case *historypb.ActivityTaskStartedEventAttributes:
			event.Attributes = &historypb.HistoryEvent_ActivityTaskStartedEventAttributes{ActivityTaskStartedEventAttributes: a}
		case *historypb.ActivityTaskCompletedEventAttributes:
			event.Attributes = &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{ActivityTaskCompletedEventAttributes: a}
		default:
			t.Fatalf("unhandled history event attributes type %T", attrs)
		}
		events = append(events, event)
		return id
	}

	mustPayloads := func(values ...any) *commonpb.Payloads {
		payloads, err := dc.ToPayloads(values...)
		require.NoError(t, err)
		return payloads
	}

	addEvent(enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, &historypb.WorkflowExecutionStartedEventAttributes{
		WorkflowType:        &commonpb.WorkflowType{Name: workflowType},
		TaskQueue:           taskQueue,
		Input:               mustPayloads(input),
		WorkflowRunTimeout:  durationpb.New(time.Hour),
		WorkflowTaskTimeout: durationpb.New(10 * time.Second),
		Attempt:             1,
	})

	addWorkflowTask := func() int64 {
		scheduledID := addEvent(enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED, &historypb.WorkflowTaskScheduledEventAttributes{
			TaskQueue:           taskQueue,
			StartToCloseTimeout: durationpb.New(10 * time.Second),
			Attempt:             1,
		})
		startedID := addEvent(enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED, &historypb.WorkflowTaskStartedEventAttributes{
			ScheduledEventId: scheduledID,
		})
		return addEvent(enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED, &historypb.WorkflowTaskCompletedEventAttributes{
			ScheduledEventId: scheduledID,
			StartedEventId:   startedID,
		})
	}

	workflowTaskCompletedID := addWorkflowTask()
	for i, step := range steps {
		// The SDK assigns each activity an ID equal to its predicted
		// ActivityTaskScheduled event ID, so the fixture must do the same for
		// the replay commands to match.
		scheduledID := int64(len(events) + 1)
		addEvent(enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED, &historypb.ActivityTaskScheduledEventAttributes{
			ActivityId:                   fmt.Sprintf("%d", scheduledID),
			ActivityType:                 &commonpb.ActivityType{Name: step.activityType},
			TaskQueue:                    taskQueue,
			StartToCloseTimeout:          durationpb.New(time.Minute),
			WorkflowTaskCompletedEventId: workflowTaskCompletedID,
		})
		if i == len(steps)-1 {
			break
		}
		startedID := addEvent(enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED, &historypb.ActivityTaskStartedEventAttributes{
			ScheduledEventId: scheduledID,
			Attempt:          1,
		})
		completedAttrs := &historypb.ActivityTaskCompletedEventAttributes{
			ScheduledEventId: scheduledID,
			StartedEventId:   startedID,
		}
		if step.result != nil {
			completedAttrs.Result = mustPayloads(step.result)
		}
		addEvent(enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED, completedAttrs)
		workflowTaskCompletedID = addWorkflowTask()
	}

	return &historypb.History{Events: events}
}

func replayTeardownHistory(t *testing.T, history *historypb.History) error {
	t.Helper()
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(TeardownCluster)
	replayer.RegisterWorkflow(TeardownNode)
	return replayer.ReplayWorkflowHistory(nil, history)
}

// TestTeardownClusterReplaysInFlightHistory pins the activity prefix recorded
// by running TeardownCluster executions. Had this test existed before #495
// (d0da1c51), which replaced the workflow's first scheduled activity
// GetClusterProvisioningContext with GetCluster and no version gate, it would
// have failed in CI instead of panicking in-flight teardowns after deploy.
func TestTeardownClusterReplaysInFlightHistory(t *testing.T) {
	clusterID := uuid.New()
	history := sequentialActivityHistory(t,
		"TeardownCluster",
		TeardownClusterInput{ClusterID: clusterID},
		[]replayActivityStep{
			{activityType: "GetCluster", result: db.Cluster{ID: clusterID, Name: "tenant-a"}},
			{activityType: "UpdateClusterConditions"},
		},
	)

	require.NoError(t, replayTeardownHistory(t, history))
}

// TestTeardownNodeReplaysInFlightHistory pins the activity prefix recorded by
// running TeardownNode executions, through the point where #495 removed the
// GetNodeProvisioningContext call (between LabelNode and UpdateNodeConditions).
func TestTeardownNodeReplaysInFlightHistory(t *testing.T) {
	nodeID := uuid.New()
	history := sequentialActivityHistory(t,
		"TeardownNode",
		TeardownNodeInput{NodeID: nodeID},
		[]replayActivityStep{
			{activityType: "GetNodePoolForNode", result: db.NodePool{Name: "workers"}},
			{activityType: "GetClusterForNode", result: db.Cluster{Name: "tenant-a"}},
			{activityType: "GetNodePoolForNode", result: db.NodePool{Name: "workers"}},
			{activityType: "LabelNode"},
			{activityType: "UpdateNodeConditions"},
		},
	)

	require.NoError(t, replayTeardownHistory(t, history))
}

// TestReplayerDetectsRemovedFirstActivity re-creates the #495 incident: a
// TeardownCluster history recorded before d0da1c51 has
// GetClusterProvisioningContext as its first scheduled activity, which current
// code replaced with GetCluster. Replay must report the nondeterminism rather
// than silently diverge — this guards the harness itself, proving the fixtures
// above actually detect this class of change.
func TestReplayerDetectsRemovedFirstActivity(t *testing.T) {
	history := sequentialActivityHistory(t,
		"TeardownCluster",
		TeardownClusterInput{ClusterID: uuid.New()},
		[]replayActivityStep{
			{activityType: "GetClusterProvisioningContext"},
		},
	)

	err := replayTeardownHistory(t, history)
	require.Error(t, err)
	require.ErrorContains(t, err, "GetClusterProvisioningContext")
	require.ErrorContains(t, err, "GetCluster")
}
