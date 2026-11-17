# leutik-tarik platform image.
#
# Static binary on distroless: no shell, no package manager, non-root by default.
# Build context only needs the compiled binary, so the build runs on a machine
# without a Go toolchain (the server has Go 1.22, the module needs 1.26).
FROM gcr.io/distroless/static-debian12:nonroot
COPY leutik-tarik /leutik-tarik
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/leutik-tarik"]
