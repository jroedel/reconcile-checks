# A computer for the self-update test: Linux, a person's account, and nothing
# that could fetch a release but the program under test. Ported from
# sion-backup's.
FROM debian:stable-slim

# ca-certificates for the trust store the harness adds its CA to; procps to
# count the program's processes. No curl, no wget.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates procps \
 && rm -rf /var/lib/apt/lists/*

# The person at the computer. Not root: the program is installed and run by
# its own user, in a folder that user can write (docs/10, "Installing").
RUN useradd --create-home --shell /bin/bash treasurer

COPY run-case /harness/run-case
RUN chmod 0755 /harness/run-case

ENTRYPOINT ["/harness/run-case"]
