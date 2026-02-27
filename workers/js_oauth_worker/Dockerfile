FROM node:20-slim

WORKDIR /app

COPY workers/js_oauth_worker/worker.mjs /app/worker.mjs

CMD ["node", "/app/worker.mjs"]
