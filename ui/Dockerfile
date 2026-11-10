# syntax=docker/dockerfile:1
#
# Build contexts:
#   - the default context is this repo's ui/ folder
#   - the named context "dashboard" is a checkout of oakestra-dashboard-v2 at the commit in
#     DASHBOARD_REF; the addon links its SDK and UI packages from source
#
#   docker buildx build --build-context dashboard=../oakestra-dashboard-v2 \
#     -t oakestra-mcp-ui ui
#
# The checkouts are laid out as /work/oakestra-dashboard-v2 and /work/oakestra-mcp/ui because
# package.json links the dashboard packages by the relative path ../../oakestra-dashboard-v2/*.

# The output is static files, so build once on the host's platform rather than under emulation.
FROM --platform=$BUILDPLATFORM node:24-slim AS build
RUN npm install --global pnpm@12.16.1
WORKDIR /work

# Host node_modules and dist would be wrong for this platform (and huge), so keep them out.
COPY --from=dashboard --exclude=**/node_modules --exclude=**/dist . oakestra-dashboard-v2/
RUN --mount=type=cache,id=pnpm,target=/root/.local/share/pnpm/store \
    cd oakestra-dashboard-v2 && pnpm install --frozen-lockfile \
      --filter "@oakestra/dashboard-sdk..." --filter "@oakestra/ui..."

WORKDIR /work/oakestra-mcp/ui
COPY package.json pnpm-lock.yaml ./
RUN --mount=type=cache,id=pnpm,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile

COPY . .
RUN pnpm build

FROM nginx:1.30-alpine
# Both variables must be defined, or the literal ${...} would end up in the nginx config.
ENV API_UPSTREAM=http://root_mcp:8090 \
    NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1
COPY docker/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /work/oakestra-mcp/ui/dist /usr/share/nginx/html
# Addon UI contract: plain HTTP on port 80, reachable as addonui-<name> on the dashboard network.
EXPOSE 80
