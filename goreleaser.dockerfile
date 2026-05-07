FROM ghcr.io/mr-pmillz/alpine-bash-tini:latest

ARG TARGETPLATFORM

COPY entrypoint.sh /entrypoint.sh
COPY $TARGETPLATFORM/gau /usr/local/bin/gau
RUN chmod +x /entrypoint.sh /usr/local/bin/gau

ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]
