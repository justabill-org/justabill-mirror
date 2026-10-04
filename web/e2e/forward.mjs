// A TCP forwarder for the smoke tests (#794): listens on localhost:<listen-port> and passes every
// connection on to <host:port>. In CI the Auth emulator is another container (auth-emulator:9099),
// and the browser must reach it on the page's own site (localhost): with the emulator on another
// site, a popup that opens before the helper iframe asks for Google's script leaves that request
// never sent. playwright.config.ts starts this only when E2E_AUTH_EMULATOR_HOST isn't on localhost.
//
// Before it listens, it connects to the target once, so a run whose emulator can't be reached
// fails at startup naming the host, not later as a sign-in timeout. Plain Node, no dependencies.
//
//   node e2e/forward.mjs 9099 auth-emulator:9099

import net from "node:net";

// How long the startup check waits for the target.
const CONNECT_TIMEOUT_MS = 10_000;

/** Prints why the forwarder can't run and exits, so Playwright stops the run at startup. */
function fail(message) {
  console.error(`forward.mjs: ${message}`);
  process.exit(1);
}

/** Splits "host:port", or fails naming the argument. */
function parseTarget(arg) {
  const at = (arg ?? "").lastIndexOf(":");
  const host = arg?.slice(0, at);
  const port = Number(arg?.slice(at + 1));
  if (at <= 0 || !Number.isInteger(port) || port <= 0) fail(`target "${arg ?? ""}" is not host:port`);
  return { host, port };
}

/** Resolves once a connection to the target opens, or fails naming it. */
function checkTarget({ host, port }) {
  return new Promise((resolve) => {
    const socket = net.connect({ host, port, timeout: CONNECT_TIMEOUT_MS });
    socket.once("connect", () => {
      socket.destroy();
      resolve();
    });
    socket.once("timeout", () => fail(`can't reach ${host}:${port}: no answer in ${CONNECT_TIMEOUT_MS} ms`));
    socket.once("error", (err) => fail(`can't reach ${host}:${port}: ${err.code ?? err.message}`));
  });
}

const listenPort = Number(process.argv[2]);
if (!Number.isInteger(listenPort) || listenPort <= 0) fail(`listen port "${process.argv[2] ?? ""}" is not a port`);
const target = parseTarget(process.argv[3]);
await checkTarget(target);

/** Pipes one browser connection to the target. Either side closing or failing ends both. */
function forward(client) {
  const upstream = net.connect(target);
  const end = () => {
    client.destroy();
    upstream.destroy();
  };
  client.on("error", end).on("close", end);
  upstream.on("error", end).on("close", end);
  client.pipe(upstream);
  upstream.pipe(client);
}

/**
 * Listens on one loopback address. The browser may resolve localhost to either, so both are tried;
 * IPv4 must work, and IPv6 is skipped where the machine has no ::1.
 */
function listen(address, optional) {
  return new Promise((resolve) => {
    const server = net.createServer(forward);
    server.once("error", (err) => {
      if (optional && (err.code === "EADDRNOTAVAIL" || err.code === "EAFNOSUPPORT")) return resolve(false);
      const why = err.code === "EADDRINUSE" ? "the port is taken" : (err.code ?? err.message);
      fail(`can't listen on localhost:${listenPort} (${address}) for ${target.host}:${target.port}: ${why}`);
    });
    server.listen(listenPort, address, () => resolve(true));
  });
}

await listen("127.0.0.1", false);
const ipv6 = await listen("::1", true);
console.log(`forward.mjs: localhost:${listenPort}${ipv6 ? "" : " (IPv4 only)"} -> ${target.host}:${target.port}`);
