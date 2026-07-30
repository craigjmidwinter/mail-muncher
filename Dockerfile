# The container image, built by goreleaser around an already-compiled binary.
# `dockers_v2` drops the right-architecture `mail-muncher` into the build
# context, so there is no Go toolchain here and nothing to compile: this file
# only assembles a runtime around a binary that already exists.
#
# Alpine rather than scratch or distroless, for one concrete reason:
# `imap.password_cmd` is mandatory on the IMAP path and internal/provider/imap
# runs it as `/bin/sh -c <command>`. An image with no shell cannot authenticate
# to IMAP at all -- not "less conveniently", it fails outright. Inside a
# container the command that yields the password is usually `printenv
# IMAP_PASSWORD` or `cat /run/secrets/imap-password` rather than a call into
# your password manager on the host, and both still need a shell to exist.
FROM alpine:3.22

# Verifying TLS to imap.fastmail.com, or to oauth2.googleapis.com on the Gmail
# path, needs a trust store. The base image is not guaranteed to carry one.
RUN apk add --no-cache ca-certificates

# How the MCP Registry proves you own this image: it reads this annotation back
# off the pushed manifest and requires it to equal the `name` in server.json.
# These two strings move together or the next publish is rejected.
LABEL io.modelcontextprotocol.server.name="io.github.craigjmidwinter/mail-muncher"

# mail-muncher never needs root. It also resolves config, state and archive
# paths under $HOME, so give it a real home directory to resolve them against
# rather than letting `~` expand to `/` and scattering dotfiles at the root.
RUN adduser -D -u 65532 -h /home/muncher muncher
USER muncher
ENV HOME=/home/muncher

# goreleaser lays the build context out as <os>/<arch>/<binary>, one subtree
# per platform it was asked to build, so there is no `mail-muncher` at the
# context root to copy. buildx sets TARGETOS and TARGETARCH per platform of the
# manifest it is assembling, which names the right subtree exactly. Declaring
# the ARGs is required -- they are only predefined, not automatically in scope.
ARG TARGETOS
ARG TARGETARCH
COPY ${TARGETOS}/${TARGETARCH}/mail-muncher /usr/local/bin/mail-muncher

ENTRYPOINT ["/usr/local/bin/mail-muncher"]

# `mcp` is the mode a container image is actually for: an MCP client starts the
# server, talks to it over stdin/stdout, and stops it. `run` and `daemon` work
# too -- override the command -- but on the host they are a cron line and a
# launchd/systemd unit, which is usually the better fit for those.
CMD ["mcp"]
