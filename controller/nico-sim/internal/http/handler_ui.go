// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package http

import nethttp "net/http"

// handleUIRoot redirects to /ui/ with a 302.
func (s *Server) handleUIRoot(w nethttp.ResponseWriter, r *nethttp.Request) {
	nethttp.Redirect(w, r, "/ui/", nethttp.StatusFound)
}

// handleUIIndex serves ui/index.html relative to the working directory.
func (s *Server) handleUIIndex(w nethttp.ResponseWriter, r *nethttp.Request) {
	nethttp.ServeFile(w, r, "ui/index.html")
}
