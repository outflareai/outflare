<?php

// The tracker's tests, with nothing to install: php tests/run.php

declare(strict_types=1);

require __DIR__ . '/../src/Tracker.php';

use Outflare\Tracker;

$failed = 0;
function check(bool $ok, string $what): void
{
    global $failed;
    if (!$ok) {
        $failed++;
        fwrite(STDERR, "FAIL: $what\n");
    }
}
function throws(callable $f, string $what): void
{
    try {
        $f();
        check(false, $what);
    } catch (\InvalidArgumentException $e) {
        check(true, $what);
    }
}

/** @return array{0: Tracker, 1: \ArrayObject} */
function tracker(array $answers = [], array $options = []): array
{
    $calls = new \ArrayObject();
    $transport = static function (string $url, string $token, string $body) use ($calls, &$answers): array {
        $calls[] = ['url' => $url, 'token' => $token, 'body' => json_decode($body, true)];
        $status = array_shift($answers) ?? 202;
        if ($status === 0) {
            throw new \RuntimeException('network down');
        }
        return [$status, 0];
    };
    $t = new Tracker($options + ['brand' => 'northpeak', 'token' => 'oft_test_not_a_real_token', 'base_url' => 'https://outflare.example/',
        'transport' => $transport, 'sleeper' => static function (float $s): void {
        }]);
    return [$t, $calls];
}

// Events are queued, batched and sent with the token.
[$t, $calls] = tracker();
$id = $t->track('u1', 'Order Completed', ['total' => 42]);
$t->identify('u1', ['plan' => 'pro']);
$t->group('u1', 'org_7');
$t->page(null, 'Pricing', [], ['anonymous_id' => 'a1']);
$t->screen(null, 'Home', [], ['anonymous_id' => 'a1']);
$t->alias('u1', 'a1');
check(count($calls) === 0, 'nothing is sent before a flush');
$t->flush();
check(count($calls) === 1, 'one batch');
$call = $calls[0];
check($call['url'] === 'https://outflare.example/v1/brands/northpeak/events', 'the brand events url');
check($call['token'] === 'oft_test_not_a_real_token', 'the token');
check($call['body']['source'] === 'php', 'the source');
check(array_column($call['body']['batch'], 'type') === ['track', 'identify', 'group', 'page', 'screen', 'alias'], 'the six kinds in order');
$first = $call['body']['batch'][0];
check($first['messageId'] === $id && $first['event'] === 'Order Completed', 'the track event');
check($first['context']['library']['name'] === 'outflare-php', 'the library');
check(preg_match('/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/', $id) === 1, 'the message id is a uuid');
check(!array_key_exists('userId', $call['body']['batch'][3]), 'an anonymous page carries no userId');
check(str_contains(json_encode($call['body']['batch'][1]), '"traits":{"plan":"pro"}'), 'traits are an object');

// flush_at sends without waiting.
[$t, $calls] = tracker([], ['flush_at' => 2]);
$t->track('u1', 'A');
$t->track('u1', 'B');
check(count($calls) === 1 && count($calls[0]['body']['batch']) === 2, 'flush_at');

// A busy server is retried; a refusal is not.
$lost = [];
[$t, $calls] = tracker([503, 0, 202, 401], ['on_error' => static function (\Throwable $e, int $n) use (&$lost): void {
    $lost[] = [$e->getMessage(), $n];
}]);
$t->track('u1', 'A');
$t->flush();
check(count($calls) === 3 && $lost === [], 'a busy server is retried');
$t->track('u1', 'B');
$t->flush();
check(count($calls) === 4 && $lost === [['the server refused the batch (401)', 1]], 'a refusal is not retried');

// Calls say what is missing.
[$t] = tracker();
throws(fn () => $t->track(null, 'A'), 'track without anyone');
throws(fn () => $t->track('u1', ''), 'track without a name');
throws(fn () => $t->track('u1', 'A', [], ['colour' => 'red']), 'an unknown option');
throws(fn () => new Tracker(['brand' => 'northpeak', 'token' => '']), 'no token');

echo $failed === 0 ? "ok\n" : "$failed failed\n";
exit($failed === 0 ? 0 : 1);
