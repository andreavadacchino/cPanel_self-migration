# Dev-mode container for Sprint 0 (Vite dev server). A production build would
# use an nginx multi-stage image; out of scope here.
FROM node:20-alpine

WORKDIR /app

COPY package.json package-lock.json* ./
RUN npm install

COPY . .

EXPOSE 5173

CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]
