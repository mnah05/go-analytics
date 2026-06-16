import http from "k6/http";
import { check } from "k6";
import { BASE_URL, TARGET_URL, createLink, createLinks } from "./lib.js";

// How many req/s can each JSON API endpoint handle?
// Routes tested: GET /health, POST /links/, GET /links/{slug}/stats, DELETE /links/{slug}
// Redirect (GET /{slug}) is not an API route and is excluded.
//
// Disable the rate limiter before running (internal/handler/router.go).
//
//   k6 run k6/benchmark.js                    # all endpoints, one after another
//   k6 run -e ENDPOINT=health k6/benchmark.js # single endpoint

const endpoint = __ENV.ENDPOINT || "all";

const readRamp = [
  { duration: "10s", target: 100 },
  { duration: "10s", target: 500 },
  { duration: "10s", target: 1000 },
  { duration: "10s", target: 2000 },
  { duration: "10s", target: 3000 },
  { duration: "10s", target: 0 },
];

const writeRamp = [
  { duration: "10s", target: 50 },
  { duration: "10s", target: 200 },
  { duration: "10s", target: 500 },
  { duration: "10s", target: 1000 },
  { duration: "10s", target: 0 },
];

function arrivalScenario(exec, stages, startTime) {
  return {
    executor: "ramping-arrival-rate",
    exec,
    startRate: Math.max(1, Math.floor(stages[0].target / 2)),
    timeUnit: "1s",
    preAllocatedVUs: 100,
    maxVUs: 300,
    stages,
    ...(startTime ? { startTime } : {}),
  };
}

const allScenarios = {
  health: arrivalScenario("health", readRamp, "0s"),
  create: arrivalScenario("create", writeRamp, "60s"),
  stats: arrivalScenario("stats", readRamp, "110s"),
  delete: arrivalScenario("deleteLink", writeRamp, "170s"),
};

function buildOptions() {
  if (endpoint === "all") {
    return { scenarios: allScenarios };
  }
  if (!allScenarios[endpoint]) {
    throw new Error(
      `Unknown ENDPOINT="${endpoint}". Use: health, create, stats, delete, all`,
    );
  }
  const { startTime, ...scenario } = allScenarios[endpoint];
  return { scenarios: { [endpoint]: scenario } };
}

export const options = buildOptions();

export function setup() {
  if (endpoint === "health" || endpoint === "create") {
    return { slugs: [] };
  }
  const count = endpoint === "delete" ? 5000 : 50;
  return { slugs: createLinks(count) };
}

export function health() {
  const res = http.get(`${BASE_URL}/health`);
  check(res, { "GET /health → 200": (r) => r.status === 200 });
}

export function create() {
  const res = createLink(`${TARGET_URL}?t=${Date.now()}-${__VU}-${__ITER}`);
  check(res, { "POST /links/ → 201": (r) => r.status === 201 });
}

export function stats(data) {
  if (data.slugs.length === 0) return;
  const slug = data.slugs[Math.floor(Math.random() * data.slugs.length)];
  const res = http.get(`${BASE_URL}/links/${slug}/stats`);
  check(res, { "GET /links/{slug}/stats → 200": (r) => r.status === 200 });
}

export function deleteLink(data) {
  const i = __VU - 1 + __ITER * 1000;
  if (i >= data.slugs.length) return;
  const res = http.del(`${BASE_URL}/links/${data.slugs[i]}`);
  check(res, { "DELETE /links/{slug} → 200": (r) => r.status === 200 });
}
