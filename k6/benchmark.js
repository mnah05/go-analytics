import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const failures = new Rate('failed_requests');
const redirectLatency = new Trend('redirect_latency');
const createLatency = new Trend('create_latency');
const statsLatency = new Trend('stats_latency');

export const options = {
  scenarios: {
    // Ramping arrival rate to find saturation point on redirects (critical path)
    throughput_ramp: {
      executor: 'ramping-arrival-rate',
      startRate: 50,
      timeUnit: '1s',
      preAllocatedVUs: 200,
      maxVUs: 300,
      stages: [
        { duration: '10s', target: 100 },   // 100 req/s
        { duration: '10s', target: 500 },   // 500 req/s
        { duration: '10s', target: 1000 },  // 1000 req/s
        { duration: '10s', target: 2000 },  // 2000 req/s
        { duration: '10s', target: 3000 },  // 3000 req/s
        { duration: '10s', target: 4000 },  // 4000 req/s
        { duration: '10s', target: 5000 },  // 5000 req/s
        { duration: '10s', target: 0 },     // cooldown
      ],
      exec: 'benchmark',
    },
  },
  thresholds: {
    failed_requests: ['rate<0.5'], // allow up to 50% failure before we declare saturation
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const TARGET_URL = 'https://example.com';
const headers = { 'Content-Type': 'application/json' };

// Pool of slugs shared across VUs for realistic redirect targets
let redirectSlugs = [];

export function setup() {
  // Pre-create slugs for redirects
  const slugs = [];
  for (let i = 0; i < 50; i++) {
    const res = http.post(`${BASE_URL}/links/`, JSON.stringify({ url: `${TARGET_URL}?n=${i}` }), { headers });
    if (res.status === 201) {
      slugs.push(res.json('data.slug'));
    }
  }
  // Also create some extra slugs that won't get deleted
  for (let i = 0; i < 50; i++) {
    const res = http.post(`${BASE_URL}/links/`, JSON.stringify({ url: `${TARGET_URL}?b=${i}` }), { headers });
    if (res.status === 201) {
      slugs.push(res.json('data.slug'));
    }
  }
  console.log(`setup: created ${slugs.length} slugs`);
  return { slugs };
}

export function benchmark(data) {
  const slugs = data.slugs;
  if (slugs.length === 0) return;

  const r = Math.random();

  // 60% redirects (critical path - what we care about most)
  if (r < 0.60) {
    const slug = slugs[Math.floor(Math.random() * slugs.length)];
    const res = http.get(`${BASE_URL}/${slug}`, { redirects: 0 });
    redirectLatency.add(res.timings.duration);
    check(res, { 'redirect 302': (r) => r.status === 302 });
    failures.add(res.status !== 302);
  }
  // 20% create links
  else if (r < 0.80) {
    const res = http.post(`${BASE_URL}/links/`, JSON.stringify({ url: `${TARGET_URL}?t=${Date.now()}` }), { headers });
    createLatency.add(res.timings.duration);
    check(res, { 'create 201': (r) => r.status === 201 });
    failures.add(res.status !== 201);
    if (res.status === 201) {
      slugs.push(res.json('data.slug'));
    }
  }
  // 15% stats
  else if (r < 0.95) {
    const slug = slugs[Math.floor(Math.random() * slugs.length)];
    const res = http.get(`${BASE_URL}/links/${slug}/stats`);
    statsLatency.add(res.timings.duration);
    check(res, { 'stats 200': (r) => r.status === 200 });
    failures.add(res.status !== 200);
  }
  // 5% delete
  else if (slugs.length > 10) { // keep minimum pool
    const idx = Math.floor(Math.random() * slugs.length);
    const slug = slugs[idx];
    const res = http.del(`${BASE_URL}/links/${slug}`);
    check(res, { 'delete 200': (r) => r.status === 200 });
    failures.add(res.status !== 200);
    slugs.splice(idx, 1);
  }
}
