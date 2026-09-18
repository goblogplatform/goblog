package wasm

// The test fixture is built with the standard Go toolchain and committed so
// `go test` needs no wasm toolchain. Rebuild after editing testdata/echo:
//
//go:generate sh -c "cd testdata/echo && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../echo.wasm ."
