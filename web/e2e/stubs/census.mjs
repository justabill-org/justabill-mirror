// A stand-in for the Census Bureau geocoder (docs/design/84-e2e-smoke-tests.md, "How to handle the
// Census geocoder"). The API's CENSUS_GEOCODER_URL points here during the smoke tests, so the API
// still makes the HTTP call and parses a Census-shaped response, and no test reaches the Census.
//
// Every address, and every point sent to the coordinates endpoint ("use my location"), resolves
// to California's 12th district, where db/fixture seats Ada Alvarez. The answer carries whichever
// district layer the API asked for ("119th Congressional Districts" in vintage ACS2025_Current,
// "120th …" in ACS2026_Current: api/internal/district layerFor), with that congress as CDSESSN, as
// the real geocoder does. Plain Node, no dependencies.
//
//   node e2e/stubs/census.mjs    # listens on E2E_CENSUS_PORT (18089)

import http from "node:http";

const PORT = Number(process.env.E2E_CENSUS_PORT || 18089);
const PATH = "/geocoder/geographies/onelineaddress";
// The coordinates endpoint, which the API derives from CENSUS_GEOCODER_URL (x = longitude, y = latitude).
const COORDINATES_PATH = "/geocoder/geographies/coordinates";

// California (FIPS 06), district 12.
const STATE_FIPS = "06";
const DISTRICT = "12";

// "119th Congressional Districts" → "119".
const LAYER = /^(\d{3})th Congressional Districts$/;

function send(res, status, body) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

function districtRecord(congress) {
  return {
    GEOID: STATE_FIPS + DISTRICT,
    CDSESSN: congress,
    STATE: STATE_FIPS,
    BASENAME: DISTRICT,
    NAME: `Congressional District ${Number(DISTRICT)}`,
    LSADC: "C2",
    FUNCSTAT: "N",
    MTFCC: "G5200",
  };
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://localhost");
  // Playwright's readiness check.
  if (url.pathname === "/health") return send(res, 200, { ok: true });
  if (req.method !== "GET" || (url.pathname !== PATH && url.pathname !== COORDINATES_PATH)) {
    return send(res, 404, { errors: ["Not found"] });
  }

  const q = url.searchParams;
  const layers = q.get("layers") ?? "";
  const congress = LAYER.exec(layers)?.[1];
  if (url.pathname === COORDINATES_PATH) return coordinates(res, q, layers, congress);

  const address = q.get("address");
  if (!address || q.get("format") !== "json" || !q.get("benchmark") || !q.get("vintage") || !congress) {
    // The real geocoder answers a malformed request with 400 and a list of errors.
    return send(res, 400, { errors: ["address, benchmark, vintage, layers and format=json are required"] });
  }

  send(res, 200, {
    result: {
      input: {
        address: { address },
        vintage: { vintageName: q.get("vintage") },
        benchmark: { benchmarkName: q.get("benchmark") },
      },
      addressMatches: [
        {
          matchedAddress: "1 E2E WAY, SAN FRANCISCO, CA, 94103",
          coordinates: { x: -122.4194, y: 37.7749 },
          addressComponents: { state: "CA", zip: "94103" },
          geographies: { [layers]: [districtRecord(congress)] },
        },
      ],
    },
  });
});

// The coordinates endpoint answers with the layers at the point directly, not address matches.
function coordinates(res, q, layers, congress) {
  const x = Number(q.get("x"));
  const y = Number(q.get("y"));
  const valid = q.get("x") && q.get("y") && Math.abs(x) <= 180 && Math.abs(y) <= 90;
  if (!valid || q.get("format") !== "json" || !q.get("benchmark") || !q.get("vintage") || !congress) {
    return send(res, 400, { errors: ["x, y, benchmark, vintage, layers and format=json are required"] });
  }
  send(res, 200, {
    result: {
      input: {
        location: { x, y },
        vintage: { vintageName: q.get("vintage") },
        benchmark: { benchmarkName: q.get("benchmark") },
      },
      geographies: { [layers]: [districtRecord(congress)] },
    },
  });
}

server.listen(PORT, () => {
  console.log(`census stub listening on http://localhost:${PORT}${PATH}`);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
