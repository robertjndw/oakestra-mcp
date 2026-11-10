# syntax=docker/dockerfile:1
#
# @oakestra/dashboard-sdk and @oakestra/ui come from GitHub Packages, which needs a token even
# for public packages. Pass one with read:packages as a build secret:
#
#   NODE_AUTH_TOKEN=$(gh auth token) docker buildx build --secret id=NODE_AUTH_TOKEN \
#     -t oakestra-mcp-ui ui

# The output is static files, so build once on the host's platform rather than under emulation.
FROM --platform=$BUILDPLATFORM node:24-slim AS build
RUN npm install --global pnpm@12.16.1
WORKDIR /work

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc ./
# Write the token to a throwaway user config so it never ends up in a layer.
RUN --mount=type=cache,id=pnpm,target=/root/.local/share/pnpm/store \
    --mount=type=secret,id=NODE_AUTH_TOKEN,env=NODE_AUTH_TOKEN,required=true \
    printf '//npm.pkg.github.com/:_authToken=%s\n' "$NODE_AUTH_TOKEN" > /tmp/npmrc \
    && NPM_CONFIG_USERCONFIG=/tmp/npmrc pnpm install --frozen-lockfile \
    && rm /tmp/npmrc

COPY . .
RUN pnpm build

FROM nginx:1.30-alpine
# Both variables must be defined, or the literal ${...} would end up in the nginx config.
ENV API_UPSTREAM=http://root_mcp:8090 \
    NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1
COPY docker/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /work/dist /usr/share/nginx/html
# Addon UI contract: plain HTTP on port 80, reachable as addonui-<name> on the dashboard network.
EXPOSE 80
