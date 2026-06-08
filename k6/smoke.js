import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

const failures = new Rate('failed_requests');

export const options = {
  vus: 1,
  iterations: 1,
  thresholds: {
    failed_requests: ['rate<1.0'],
    http_req_duration: ['max<5000'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const TARGET_URL = 'https://example.com';

export default function () {
  // 1. Health check
  let res = http.get(`${BASE_URL}/health`);
  check(res, {
    'health status is 200': (r) => r.status === 200,
    'health body has database status': (r) => r.json('status.database') === 'up',
    'health body has redis status': (r) => r.json('status.redis') === 'up',
  });
  failures.add(res.status !== 200);
  console.log(`health: ${res.status} (${res.body})`);

  // 2. Create a short link
  const payload = JSON.stringify({ url: TARGET_URL });
  const headers = { 'Content-Type': 'application/json' };
  res = http.post(`${BASE_URL}/links/`, payload, { headers });
  check(res, {
    'create link status is 201': (r) => r.status === 201,
    'create link returns success true': (r) => r.json('success') === true,
    'create link returns data.slug': (r) => r.json('data.slug') !== undefined,
  });
  failures.add(res.status !== 201);

  let slug;
  if (res.status === 201) {
    slug = res.json('data.slug');
    console.log(`created link slug: ${slug}`);
  } else {
    console.log(`create failed: ${res.status} ${res.body}`);
    return;
  }

  // 3. Visit the short link (redirect + click tracking)
  res = http.get(`${BASE_URL}/${slug}`, { redirects: 0 });
  check(res, {
    'redirect status is 302': (r) => r.status === 302,
    'redirect location is target': (r) => r.headers['Location'] === TARGET_URL,
  });
  failures.add(res.status !== 302);
  console.log(`redirect: ${res.status} -> ${res.headers['Location'] || 'none'}`);

  // 4. Check stats for the link
  res = http.get(`${BASE_URL}/links/${slug}/stats`);
  check(res, {
    'stats status is 200': (r) => r.status === 200,
    'stats returns slug': (r) => r.json('data.slug') === slug,
  });
  failures.add(res.status !== 200);
  console.log(`stats: ${res.status} (clicks: ${res.json('data.total_clicks')})`);

  // 5. Delete the link
  res = http.del(`${BASE_URL}/links/${slug}`);
  check(res, {
    'delete status is 200': (r) => r.status === 200,
    'delete returns success true': (r) => r.json('success') === true,
  });
  failures.add(res.status !== 200);
  console.log(`delete: ${res.status} (${res.body})`);
}
