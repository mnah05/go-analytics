import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const failures = new Rate('failed_requests');
const redirectLatency = new Trend('redirect_latency');
const createLatency = new Trend('create_latency');
const statsLatency = new Trend('stats_latency');

export const options = {
  stages: [
    { duration: '20s', target: 10 },
    { duration: '30s', target: 25 },
    { duration: '20s', target: 50 },
    { duration: '30s', target: 50 },
    { duration: '20s', target: 0 },
  ],
  thresholds: {
    failed_requests: ['rate<0.05'],
    http_req_duration: ['p(95)<2000'],
    redirect_latency: ['p(95)<500'],
    create_latency: ['p(95)<3000'],
    stats_latency: ['p(95)<2000'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const TARGET_URL = 'https://example.com';
const headers = { 'Content-Type': 'application/json' };

// Pre-create a pool of slugs to redirect to during the test
const slugPool = [];

export function setup() {
  const slugs = [];
  for (let i = 0; i < 20; i++) {
    const res = http.post(`${BASE_URL}/links/`, JSON.stringify({ url: `${TARGET_URL}?n=${i}` }), { headers });
    if (res.status === 201) {
      slugs.push(res.json('data.slug'));
    }
  }
  console.log(`pre-created ${slugs.length} slugs for load test`);
  return { slugs };
}

export default function (data) {
  const slugs = data.slugs;

  // 40% redirects (the critical path)
  if (Math.random() < 0.4 && slugs.length > 0) {
    const slug = slugs[Math.floor(Math.random() * slugs.length)];
    const res = http.get(`${BASE_URL}/${slug}`, { redirects: 0 });
    redirectLatency.add(res.timings.duration);
    check(res, { 'redirect returns 302': (r) => r.status === 302 });
    failures.add(res.status !== 302);
  }

  // 25% create new links
  if (Math.random() < 0.25) {
    const res = http.post(`${BASE_URL}/links/`, JSON.stringify({ url: `${TARGET_URL}?t=${Date.now()}` }), { headers });
    createLatency.add(res.timings.duration);
    check(res, { 'create returns 201': (r) => r.status === 201 });
    failures.add(res.status !== 201);
    if (res.status === 201) {
      const slug = res.json('data.slug');
      http.get(`${BASE_URL}/${slug}`, { redirects: 0 });
    }
  }

  // 20% stats lookups
  if (Math.random() < 0.2 && slugs.length > 0) {
    const slug = slugs[Math.floor(Math.random() * slugs.length)];
    const res = http.get(`${BASE_URL}/links/${slug}/stats`);
    statsLatency.add(res.timings.duration);
    check(res, { 'stats returns 200': (r) => r.status === 200 });
    failures.add(res.status !== 200);
  }

  // 15% deletes
  if (Math.random() < 0.15 && slugs.length > 0) {
    const idx = Math.floor(Math.random() * slugs.length);
    const slug = slugs[idx];
    const res = http.del(`${BASE_URL}/links/${slug}`);
    check(res, { 'delete returns 200': (r) => r.status === 200 });
    failures.add(res.status !== 200);
    slugs.splice(idx, 1);
  }

  sleep(Math.random() * 0.5 + 0.1);
}
