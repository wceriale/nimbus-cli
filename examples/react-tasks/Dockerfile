# better-sqlite3 ships prebuilt binaries, but npm still runs node-gyp on it, which needs Python
# (not in node:24-slim) even when there's nothing to compile. --ignore-scripts skips that step.

# Build stage: install all dependencies and build the React app into dist/.
FROM node:24-slim AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY index.html vite.config.js ./
COPY src ./src
RUN npm run build

# Runtime stage: production dependencies, the built frontend, and the server.
FROM node:24-slim
WORKDIR /app
ENV NODE_ENV=production
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts && npm cache clean --force
COPY --from=build /app/dist ./dist
COPY server ./server
# Nimbus runs this as uid 10001 with a read-only filesystem. Only $NIMBUS_DATA_DIR (/data) and
# /tmp are writable. Run node directly: `npm start` needs a writable home directory.
EXPOSE 8080
CMD ["node", "server/server.js"]
