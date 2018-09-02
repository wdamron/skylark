#!/bin/bash

# Copyright 2018 West Damron. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -e

# this script must be executed from the containing directory

go install ./util
OUTPUT=$(go run ./gen/gen.go)

echo "$OUTPUT" > ./kinds/zz_generated.kinds.go
go fmt ./kinds
go vet ./kinds
go install ./kinds
