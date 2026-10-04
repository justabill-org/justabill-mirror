# Firebase Auth emulator for local sign-in and CI (social auth design, #55).
# The Auth emulator is plain Node, so the image needs no Java.
# Node 24, like web/.nvmrc, CI and Vercel (#737).
FROM node:24.21.0-alpine3.24@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1

ARG FIREBASE_TOOLS_VERSION=15.31.0
RUN npm install --global --no-fund --no-audit "firebase-tools@${FIREBASE_TOOLS_VERSION}" \
  && npm cache clean --force

USER node
WORKDIR /home/node/emulator
COPY --chown=node:node firebase.json ./

# A "demo-" project ID never reaches real Google services, so no credentials are needed.
ENV EMULATOR_PROJECT=demo-justabill
EXPOSE 9099

HEALTHCHECK --interval=2s --timeout=3s --start-period=30s --retries=30 \
  CMD wget -qO- http://127.0.0.1:9099/ >/dev/null || exit 1

CMD ["sh", "-c", "exec firebase emulators:start --only auth --project \"$EMULATOR_PROJECT\""]
