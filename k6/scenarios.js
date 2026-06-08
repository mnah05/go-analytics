import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const failures = new Rate('failed_requests');
const redirectLatency = new Trend('redirect_latency');
const createLatency = new Trend('create_latency');

export const options = {
  scenarios: {
    // Scenario 1: Steady link creation + redirect traffic
    steady_traffic: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '20s', target: 30 },
        { duration: '60s', target: 50 },
        { duration: '20s', target: 0 },
      ],
      gracefulRampDown: '10s',
      exec: 'browsing',
    },
    // Scenario 2: Burst redirect traffic (spike)
    burst_traffic: {
      executor: 'ramping-arrival-rate',
      startRate: 10,
      timeUnit: '1s',
      preAllocatedVUs: 50,
      maxVUs: 100,
      stages: [
        { duration: '10s', target: 10 },
        { duration: '20s', target: 100 },
        { duration: '10s', target: 10 },
      ],
      gracefulRampDown: '5s',
      exec: 'clickRedirect',
    },
  },
  thresholds: {
    failed_requests: ['rate<0.05'],
    http_req_duration: ['p(95)<3000'],
    redirect_latency: ['p(95)<1000'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const TARGET_URL = 'https://example.com';
const headers = { 'Content-Type': 'application/json' };

let sharedSlugs = [];

export function setup() {
  // Create a batch of links for the burst scenario to use
  const slugs = [];
  for (let i = 0; i < 30; i++) {
    const res = http.post(`${BASE_URL}/links/`, JSON.stringify({ url: `${TARGET_URL}?batch=${i}` }), { headers });
    if (res.status === 201) {
      slugs.push(res.json('data.slug'));
    }
  }
  console.log(`setup: created ${slugs.length} slugs for burst traffic`);
  return { slugs };
}

export function browsing(data) {
  const r = Math.random();
  const slugs = data.slugs;

  // 30% redirects
  if (r < 0.3 && slugs.length > 0) {
    const slug = slugs[Math.floor(Math.random() * slugs.length)];
    const res = http.get(`${BASE_URL}/${slug}`, { redirects: 0 });
    redirectLatency.add(res.timings.duration);
    check(res, { 'browse redirect 302': (r) => r.status === 302 });
    failures.add(res.status !== 302);
  }

  // 30% create new links
  if (r < 0.6) {
    const res = http.post(`${BASE_URL}/links/`, JSON.stringify({ url: `${TARGET_URL}?u=${Date.now()}` }), { headers });
    createLatency.add(res.timings.duration);
    check(res, { 'browse create 201': (r) => r.status === 201 });
    failures.add(res.status !== 201);
    if (res.status === 201) {
      const slug = res.json('data.slug');
      slugs.push(slug);
    }
  }

  // 20% stats
  if (r < 0.8 && slugs.length > 0) {
    const slug = slugs[Math.floor(Math.random() * slugs.length)];
    const res = http.get(`${BASE_URL}/links/${slug}/stats`);
    check(res, { 'browse stats 200': (r) => r.status === 200 });
    failures.add(res.status !== 200);
  }

  // 20% delete
  if (slugs.length > 0) {
    const idx = Math.floor(Math.random() * slugs.length);
    const slug = slugs[idx];
    const res = http.del(`${BASE_URL}/links/${slug}`);
    check(res, { 'browse delete 200': (r) => r.status === 200 });
    failures.add(res.status !== 200);
    slugs.splice(idx, 1);
  }

  sleep(Math.random() * 0.3 + 0.1);
}

export function clickRedirect(data) {
  // High-frequency redirects only — simulate a viral link getting hammered
  const slugs = data.slugs;
  if (slugs.length === 0) return;

  const slug = slugs[Math.floor(Math.random() * slugs.length)];
  const res = http.get(`${BASE_URL}/${slug}`, { redirects: 0 });
  redirectLatency.add(res.timings.duration);
  check(res, { 'burst redirect 302': (r) => r.status === 302 });
  failures.add(res.status !== 302);
}
