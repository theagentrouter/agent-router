// The test dynamic module is a separate Go module: it is built as a C shared library with cgo, and the SDK version
// must match the Envoy version it is loaded into (see Dockerfile).
module github.com/envoyproxy/ai-gateway/tests/internal/testdynamicmodule

go 1.27.3

require github.com/envoyproxy/envoy/source/extensions/dynamic_modules v0.0.0-20261027023526-56e0748e3dea
