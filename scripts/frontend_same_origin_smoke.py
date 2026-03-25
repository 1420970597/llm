#!/usr/bin/env python3
import atexit
import fcntl
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from http.cookiejar import CookieJar
from urllib.request import HTTPCookieProcessor, build_opener

base = sys.argv[1] if len(sys.argv) > 1 else 'http://127.0.0.1:3210'
api = f'{base}/api'
ts = int(time.time())
request_timeout = int(os.getenv('SMOKE_REQUEST_TIMEOUT', '300'))
stage_timeout = int(os.getenv('SMOKE_STAGE_TIMEOUT', '1800'))
lock_path = os.getenv('SMOKE_LOCK_PATH', '/tmp/frontend_same_origin_smoke.lock')
jar = CookieJar()
opener = build_opener(HTTPCookieProcessor(jar))

lock_file = open(lock_path, 'w')
try:
    fcntl.flock(lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
except BlockingIOError:
    print(f'smoke already running; skipping duplicate invocation (lock={lock_path})', file=sys.stderr, flush=True)
    sys.exit(3)


@atexit.register
def release_lock():
    try:
        fcntl.flock(lock_file, fcntl.LOCK_UN)
    except OSError:
        pass
    lock_file.close()


def log(message):
    print(message, flush=True)


def request_json(method, path, payload=None, retries=0):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(api + path, data=data, method=method, headers={'Content-Type': 'application/json'})
    attempts = retries + 1
    for attempt in range(1, attempts + 1):
        started = time.time()
        log(f'[request] {method} {path} attempt={attempt}/{attempts}')
        try:
            with opener.open(req, timeout=request_timeout) as resp:
                raw = resp.read().decode()
                elapsed = time.time() - started
                log(f'[response] {method} {path} status={resp.status} elapsed={elapsed:.2f}s')
                return resp.status, json.loads(raw) if raw else None
        except urllib.error.HTTPError as e:
            elapsed = time.time() - started
            body = e.read().decode()
            try:
                parsed = json.loads(body)
            except Exception:
                parsed = body
            log(f'[http-error] {method} {path} status={e.code} elapsed={elapsed:.2f}s body={parsed}')
            parsed_text = str(parsed).lower()
            transient = e.code in (429, 500, 502, 503, 504) or 'connection reset by peer' in parsed_text
            if transient and attempt < attempts:
                backoff = min(3 * attempt, 10)
                log(f'[retry] {method} {path} retrying after {backoff}s due_to_status={e.code}')
                time.sleep(backoff)
                continue
            raise RuntimeError(f'{method} {path} -> {e.code}: {parsed}')
        except (TimeoutError, urllib.error.URLError) as e:
            elapsed = time.time() - started
            log(f'[network-error] {method} {path} elapsed={elapsed:.2f}s error={repr(e)}')
            if attempt < attempts:
                time.sleep(3)
                continue
            raise


def wait_until(label, path, timeout=600, interval=5):
    start = time.time()
    last = None
    while time.time() - start < timeout:
        _, last = request_json('GET', path)
        if last:
            return last
        time.sleep(interval)
    raise TimeoutError(f'{label} timeout; last={last}')


def wait_until_predicate(label, path, predicate, timeout=600, interval=5):
    start = time.time()
    last = None
    while time.time() - start < timeout:
        _, last = request_json('GET', path)
        if predicate(last):
            return last
        time.sleep(interval)
    raise TimeoutError(f'{label} timeout; last={last}')


def choose_real_provider(providers):
    for provider in providers:
        if provider.get('isActive') and provider.get('providerType') != 'mock':
            return provider
    raise RuntimeError('No active non-mock provider found. Configure a real provider first.')


def choose_storage_profile(profiles):
    for profile in profiles:
        if profile.get('isActive') and profile.get('isDefault'):
            return profile
    for profile in profiles:
        if profile.get('isActive'):
            return profile
    raise RuntimeError('No active storage profile found. Configure storage first.')


def strategy_cost(strategy):
    return (
        int(strategy.get('domainCount', 0))
        * int(strategy.get('questionsPerDomain', 0))
        * max(1, int(strategy.get('answerVariants', 1)))
    )


def choose_strategy(strategies):
    if not strategies:
        raise RuntimeError('No generation strategy found. Configure a strategy first.')
    return min(strategies, key=strategy_cost)


# admin login
request_json('POST', '/v1/auth/login', {'email': 'admin@company.com', 'password': 'admin123456'})

_, providers = request_json('GET', '/v1/admin/providers')
_, storage_profiles = request_json('GET', '/v1/admin/storage-profiles')
_, strategies = request_json('GET', '/v1/admin/generation-strategies')

provider = choose_real_provider(providers)
storage = choose_storage_profile(storage_profiles)
strategy = choose_strategy(strategies)

_, estimate = request_json('POST', '/v1/datasets/plans/estimate', {
    'rootKeyword': f'真实链路验证-{ts}',
    'targetSize': 12,
    'strategyId': strategy['id'],
})
_, dataset = request_json('POST', '/v1/datasets', {
    'name': f'Real LLM Smoke Dataset {ts}',
    'rootKeyword': f'真实链路验证-{ts}',
    'targetSize': 12,
    'strategyId': strategy['id'],
    'providerId': provider['id'],
    'storageProfileId': storage['id'],
    'status': 'draft',
    'estimate': estimate,
})

dataset_id = dataset['id']
log(f'smoke start dataset_id={dataset_id} provider={provider.get("id")} strategy={strategy.get("id")} timeout={request_timeout}s')
_, graph = request_json('POST', f'/v1/datasets/{dataset_id}/domains/generate', retries=2)
request_json('POST', f'/v1/datasets/{dataset_id}/domains/confirm', retries=2)
request_json('POST', f'/v1/datasets/{dataset_id}/questions/generate', retries=2)
questions = wait_until('questions', f'/v1/datasets/{dataset_id}/questions', timeout=stage_timeout)
request_json('POST', f'/v1/datasets/{dataset_id}/reasoning/generate', retries=2)
reasoning = wait_until('reasoning', f'/v1/datasets/{dataset_id}/reasoning', timeout=stage_timeout)
request_json('POST', f'/v1/datasets/{dataset_id}/rewards/generate', retries=2)
rewards = wait_until('rewards', f'/v1/datasets/{dataset_id}/rewards', timeout=stage_timeout)
request_json('POST', f'/v1/datasets/{dataset_id}/export', retries=2)
export_dataset = wait_until_predicate(
    'dataset export status',
    f'/v1/datasets/{dataset_id}',
    lambda payload: bool(payload) and payload.get('dataset', {}).get('status') == 'export_generated',
    timeout=stage_timeout,
)
_, pipeline_progress = request_json('GET', f'/v1/datasets/{dataset_id}/pipeline/progress')
artifacts = wait_until('artifacts', f'/v1/datasets/{dataset_id}/export', timeout=stage_timeout)
_, runtime = request_json('GET', '/v1/platform/runtime')

export_stage = next((item for item in pipeline_progress.get('stages', []) if item.get('key') == 'export'), None)
if export_stage is None:
    raise RuntimeError('Pipeline progress missing export stage')
if len(artifacts) > 0 and export_stage.get('state') != 'completed':
    raise RuntimeError(f"Export stage should be completed when artifacts exist: {export_stage}")
if export_dataset.get('dataset', {}).get('status') == 'export_generated' and len(artifacts) == 0:
    if export_stage.get('state') != 'in_progress':
        raise RuntimeError(f"Export stage should be in_progress before artifact visible: {export_stage}")
    if int(pipeline_progress.get('completionPercent', 0)) >= 100:
        raise RuntimeError(f"Completion percent should be < 100 before artifact visible: {pipeline_progress}")

print(json.dumps({
    'datasetId': dataset_id,
    'provider': {
        'id': provider['id'],
        'name': provider.get('name'),
        'providerType': provider.get('providerType'),
        'model': provider.get('model'),
    },
    'storageProfile': {
        'id': storage['id'],
        'name': storage.get('name'),
        'bucket': storage.get('bucket'),
    },
    'strategy': {
        'id': strategy['id'],
        'name': strategy.get('name'),
        'domainCount': strategy.get('domainCount'),
        'questionsPerDomain': strategy.get('questionsPerDomain'),
    },
    'domainCount': len(graph['domains']),
    'questionCount': len(questions),
    'reasoningCount': len(reasoning),
    'rewardCount': len(rewards),
    'artifactCount': len(artifacts),
    'queueDepth': runtime['queueDepth'],
}, ensure_ascii=False, indent=2))
