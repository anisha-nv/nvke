# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

.PHONY: test
test: ## Run the renderer, release spec and executor tests in testdata/.
	./testdata/test.sh
	./testdata/test_release_spec.sh
	./testdata/test_nke_deploy.sh
