#!/usr/bin/env python3
import json
import sys
import time
import urllib.error
import urllib.request
from http.cookiejar import CookieJar
from urllib.request import HTTPCookieProcessor, build_opener

base = sys.argv[1] if len(sys.argv) > 1 else 'http://127.0.0.1:3210'
api = f'{base}/api'
ts = int(time.time())
jar = CookieJar()
opener = build_opener(HTTPCookieProcessor(jar))


def request_json(method, path, payload=None):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(api + path, data=data, method=method, headers={'Content-Type': 'application/json'})
    try:
        with opener.open(req, timeout=30) as resp:
            raw = resp.read().decode()
            return resp.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        body = e.read().decode()
        try:
            parsed = json.loads(body)
        except Exception:
            parsed = body
        raise RuntimeError(f'{method} {path} -> {e.code}: {parsed}')


def wait_until(label, path, timeout=90, interval=2):
    start = time.time()
    last = None
    while time.time() - start < timeout:
        _, last = request_json('GET', path)
        if last:
            return last
        time.sleep(interval)
    raise TimeoutError(f'{label} timeout; last={last}')

# admin login if supported; older versions will just proceed without auth
try:
    request_json('POST', '/v1/auth/login', {'email': 'admin@company.com', 'password': 'admin123456'})
except Exception:
    pass

_, provider = request_json('POST', '/v1/admin/providers', {
    'name': f'Smoke Mock Provider {ts}',
    'baseUrl': '',
    'model': 'mock-gpt',
    'providerType': 'mock',
    'maxConcurrency': 4,
    'timeoutSeconds': 120,
    'isActive': True,
    'apiKey': '',
})
_, storage = request_json('POST', '/v1/admin/storage-profiles', {
    'name': f'Smoke Storage {ts}',
    'provider': 'minio',
    'endpoint': 'http://minio:9000',
    'region': 'us-east-1',
    'bucket': f'smoke-{ts}',
    'accessKeyId': 'minioadmin',
    'secretAccessKey': 'minioadmin',
    'usePathStyle': True,
    'isDefault': False,
})
_, strategy = request_json('POST', '/v1/admin/generation-strategies', {
    'name': f'Smoke Strategy {ts}',
    'description': 'frontend smoke verification',
    'domainCount': 6,
    'questionsPerDomain': 2,
    'answerVariants': 1,
    'rewardVariants': 1,
    'planningMode': 'balanced',
    'isDefault': False,
})
request_json('POST', '/v1/admin/prompts', {
    'name': f'Smoke Prompt {ts}',
    'stage': 'domain-generation',
    'version': f'smoke-{ts}',
    'systemPrompt': 'You are a smoke-test prompt.',
    'userPrompt': 'Return concise structured data.',
    'isActive': True,
})
_, estimate = request_json('POST', '/v1/datasets/plans/estimate', {
    'rootKeyword': f'军事-{ts}',
    'targetSize': 12,
    'strategyId': strategy['id'],
})
_, dataset = request_json('POST', '/v1/datasets', {
    'name': f'Smoke Dataset {ts}',
    'rootKeyword': f'军事-{ts}',
    'targetSize': 12,
    'strategyId': strategy['id'],
    'providerId': provider['id'],
    'storageProfileId': storage['id'],
    'status': 'draft',
    'estimate': estimate,
})

dataset_id = dataset['id']
_, graph = request_json('POST', f'/v1/datasets/{dataset_id}/domains/generate')
request_json('POST', f'/v1/datasets/{dataset_id}/domains/confirm')
request_json('POST', f'/v1/datasets/{dataset_id}/questions/generate')
questions = wait_until('questions', f'/v1/datasets/{dataset_id}/questions')
request_json('POST', f'/v1/datasets/{dataset_id}/reasoning/generate')
reasoning = wait_until('reasoning', f'/v1/datasets/{dataset_id}/reasoning')
request_json('POST', f'/v1/datasets/{dataset_id}/rewards/generate')
rewards = wait_until('rewards', f'/v1/datasets/{dataset_id}/rewards')
request_json('POST', f'/v1/datasets/{dataset_id}/export')
artifacts = wait_until('artifacts', f'/v1/datasets/{dataset_id}/export')
_, runtime = request_json('GET', '/v1/platform/runtime')
print(json.dumps({
    'datasetId': dataset_id,
    'domainCount': len(graph['domains']),
    'questionCount': len(questions),
    'reasoningCount': len(reasoning),
    'rewardCount': len(rewards),
    'artifactCount': len(artifacts),
    'queueDepth': runtime['queueDepth'],
}, ensure_ascii=False, indent=2))
