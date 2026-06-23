FROM gcr.io/distroless/static-debian12:nonroot

COPY bin/fortigate-external-dns /fortigate-external-dns

USER 65532:65532
ENTRYPOINT ["/fortigate-external-dns"]
