# UI image: the built console, and for the hosted deployment the landing page,
# served by unprivileged nginx.
#
# The build output is produced outside this image, so the image build stays
# seconds long and reuses the same artifact CI already tested:
#   npm -w @pds/ui run build                    # dist/app, the console
#   scripts/build-landing.sh apps/ui/dist/landing  # dist/landing, hosted only
#   docker build -t ghcr.io/percona/percona-services-ui:dev apps/ui
#
# The server config comes from the chart ConfigMap or the Compose nginx file
# at /etc/nginx/conf.d/default.conf. Both serve the console under /app/ and
# proxy /api to the adapter; only the Compose one serves the landing page.
FROM nginxinc/nginx-unprivileged:1.27-alpine

COPY dist /usr/share/nginx/html

# 101 is the nginx user in this image, matched by the chart security context.
USER 101
EXPOSE 8080
