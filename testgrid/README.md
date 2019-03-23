# Testgrid

This directory is a fork of testgrid/ utilities from k8s.io/test-infra that don't
depend on bazel.

To update the proto files, run

    bazel build //testgrid/cmd/...

from `k8s.io/test-infra` and then copy the `*.pb.go` files and `.proto` files from
the subdirs into this.

