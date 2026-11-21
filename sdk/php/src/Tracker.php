<?php

declare(strict_types=1);

namespace Outflare;

/**
 * Sending product events to Outflare from a PHP server.
 *
 *   $tracker = new \Outflare\Tracker(['brand' => 'northpeak']); // reads OUTFLARE_TOKEN and OUTFLARE_URL
 *   $tracker->identify('u_123', ['plan' => 'pro']);
 *   $tracker->track('u_123', 'Order Completed', ['total' => 42]);
 *
 * Calls are queued in memory and sent in batches to
 * POST /v1/brands/{brand}/events: once flush_at wait, when you call
 * flush(), and when the request ends. A batch the server cannot take is
 * tried again; a refusal (a bad token, no such brand) is not.
 */
final class Tracker
{
    public const VERSION = '0.110.0';
    private const MAX_BATCH = 500;

    private string $url;
    private string $token;
    private int $flushAt;
    private int $maxRetries;
    /** @var null|callable(\Throwable, int): void */
    private $onError;
    /** @var callable(string, string, string): array{0: int, 1: int} */
    private $transport;
    /** @var callable(float): void */
    private $sleeper;
    /** @var list<array<string, mixed>> */
    private array $queue = [];

    /**
     * @param array{brand?: string, token?: string, base_url?: string, flush_at?: int, max_retries?: int,
     *     on_error?: callable, transport?: callable, sleeper?: callable} $options
     *   token is an API token with customers.manage (default OUTFLARE_TOKEN); keep it on your servers.
     *   on_error hears about batches that were given up on, with how many events were lost.
     */
    public function __construct(array $options)
    {
        $token = $options['token'] ?? (getenv('OUTFLARE_TOKEN') ?: '');
        $brand = $options['brand'] ?? '';
        if ($token === '') {
            throw new \InvalidArgumentException('Outflare\Tracker needs a token: pass token or set OUTFLARE_TOKEN');
        }
        if ($brand === '') {
            throw new \InvalidArgumentException('Outflare\Tracker needs a brand: its id or slug');
        }
        $base = rtrim($options['base_url'] ?? (getenv('OUTFLARE_URL') ?: 'https://app.outflare.ai'), '/');
        $this->url = $base . '/v1/brands/' . rawurlencode($brand) . '/events';
        $this->token = $token;
        $this->flushAt = max(1, min($options['flush_at'] ?? 20, self::MAX_BATCH));
        $this->maxRetries = $options['max_retries'] ?? 3;
        $this->onError = $options['on_error'] ?? null;
        $this->transport = $options['transport'] ?? [$this, 'post'];
        $this->sleeper = $options['sleeper'] ?? static function (float $seconds): void {
            usleep((int) ($seconds * 1_000_000));
        };
        register_shutdown_function([$this, 'flush']);
    }

    /**
     * Something a person did.
     *
     * @param array<string, mixed> $properties
     * @param array{anonymous_id?: string, timestamp?: \DateTimeInterface|string, message_id?: string, context?: array<string, mixed>} $options
     */
    public function track(?string $userId, string $event, array $properties = [], array $options = []): string
    {
        if ($event === '') {
            throw new \InvalidArgumentException('track needs an event name');
        }
        return $this->add('track', $userId, ['event' => $event, 'properties' => (object) $properties], $options);
    }

    /**
     * Who a person is.
     *
     * @param array<string, mixed> $traits
     * @param array<string, mixed> $options
     */
    public function identify(string $userId, array $traits = [], array $options = []): string
    {
        if ($userId === '') {
            throw new \InvalidArgumentException('identify needs a user id');
        }
        return $this->add('identify', $userId, ['traits' => (object) $traits], $options);
    }

    /**
     * The company a person belongs to.
     *
     * @param array<string, mixed> $traits
     * @param array<string, mixed> $options
     */
    public function group(?string $userId, string $groupId, array $traits = [], array $options = []): string
    {
        if ($groupId === '') {
            throw new \InvalidArgumentException('group needs a group id');
        }
        return $this->add('group', $userId, ['groupId' => $groupId, 'traits' => (object) $traits], $options);
    }

    /**
     * A page someone saw.
     *
     * @param array<string, mixed> $properties
     * @param array<string, mixed> $options
     */
    public function page(?string $userId, ?string $name = null, array $properties = [], array $options = []): string
    {
        return $this->add('page', $userId, ['name' => $name, 'properties' => (object) $properties], $options);
    }

    /**
     * A screen someone saw in an app.
     *
     * @param array<string, mixed> $properties
     * @param array<string, mixed> $options
     */
    public function screen(?string $userId, ?string $name = null, array $properties = [], array $options = []): string
    {
        return $this->add('screen', $userId, ['name' => $name, 'properties' => (object) $properties], $options);
    }

    /**
     * An id a person had before: joins $previousId to $userId.
     *
     * @param array<string, mixed> $options
     */
    public function alias(string $userId, string $previousId, array $options = []): string
    {
        if ($userId === '' || $previousId === '') {
            throw new \InvalidArgumentException('alias needs a user id and a previous id');
        }
        return $this->add('alias', $userId, ['previousId' => $previousId], $options);
    }

    /** Send everything waiting now. Returns once it was sent or given up on. */
    public function flush(): void
    {
        while ($this->queue !== []) {
            $batch = array_splice($this->queue, 0, self::MAX_BATCH);
            try {
                $this->deliver($batch);
            } catch (\Throwable $e) {
                if ($this->onError !== null) {
                    ($this->onError)($e, count($batch));
                }
            }
        }
    }

    /**
     * @param array<string, mixed> $fields
     * @param array<string, mixed> $options
     */
    private function add(string $type, ?string $userId, array $fields, array $options): string
    {
        $anonymousId = (string) ($options['anonymous_id'] ?? '');
        $userId = (string) $userId;
        if ($userId === '' && $anonymousId === '') {
            throw new \InvalidArgumentException($type . ' needs a user id or an anonymous_id');
        }
        $unknown = array_diff(array_keys($options), ['anonymous_id', 'timestamp', 'message_id', 'context']);
        if ($unknown !== []) {
            throw new \InvalidArgumentException('unexpected options: ' . implode(', ', $unknown));
        }
        $timestamp = $options['timestamp'] ?? new \DateTimeImmutable('now', new \DateTimeZone('UTC'));
        if ($timestamp instanceof \DateTimeInterface) {
            $timestamp = $timestamp->format('Y-m-d\TH:i:s.vP');
        }
        $messageId = $options['message_id'] ?? self::uuid();
        $context = $options['context'] ?? [];
        $context['library'] = ['name' => 'outflare-php', 'version' => self::VERSION];
        $message = ['type' => $type, 'messageId' => $messageId, 'timestamp' => $timestamp, 'context' => $context] + $fields;
        if ($userId !== '') {
            $message['userId'] = $userId;
        }
        if ($anonymousId !== '') {
            $message['anonymousId'] = $anonymousId;
        }
        $this->queue[] = $message;
        if (count($this->queue) >= $this->flushAt) {
            $this->flush();
        }
        return $messageId;
    }

    /** @param list<array<string, mixed>> $batch */
    private function deliver(array $batch): void
    {
        $body = json_encode(['source' => 'php', 'batch' => $batch], JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES);
        for ($attempt = 0; ; $attempt++) {
            $status = 0;
            $retryAfter = 0;
            try {
                [$status, $retryAfter] = ($this->transport)($this->url, $this->token, $body);
                if ($status >= 200 && $status < 300) {
                    return;
                }
            } catch (\Throwable $e) {
                if ($attempt >= $this->maxRetries) {
                    throw new \RuntimeException('the server could not be reached', 0, $e);
                }
            }
            // A refusal (a bad token, no such brand) won't change on a retry.
            if ($status >= 400 && $status < 500 && $status !== 408 && $status !== 429) {
                throw new \RuntimeException("the server refused the batch ($status)");
            }
            if ($attempt >= $this->maxRetries) {
                throw new \RuntimeException("the server could not take the batch ($status)");
            }
            ($this->sleeper)(min($retryAfter > 0 ? (float) $retryAfter : 0.5 * (2 ** $attempt), 30.0));
        }
    }

    /** @return array{0: int, 1: int} the status and the Retry-After seconds */
    private function post(string $url, string $token, string $body): array
    {
        $retryAfter = 0;
        $ch = curl_init($url);
        curl_setopt_array($ch, [
            CURLOPT_POST => true,
            CURLOPT_POSTFIELDS => $body,
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_CONNECTTIMEOUT => 5,
            CURLOPT_TIMEOUT => 10,
            CURLOPT_HTTPHEADER => [
                'Content-Type: application/json',
                'Authorization: Bearer ' . $token,
                'User-Agent: outflare-php/' . self::VERSION,
            ],
            CURLOPT_HEADERFUNCTION => static function ($ch, string $line) use (&$retryAfter): int {
                if (stripos($line, 'retry-after:') === 0) {
                    $retryAfter = (int) trim(substr($line, 12));
                }
                return strlen($line);
            },
        ]);
        $ok = curl_exec($ch);
        $status = (int) curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
        $error = curl_error($ch);
        curl_close($ch);
        if ($ok === false) {
            throw new \RuntimeException($error);
        }
        return [$status, $retryAfter];
    }

    private static function uuid(): string
    {
        $b = random_bytes(16);
        $b[6] = chr((ord($b[6]) & 0x0f) | 0x40);
        $b[8] = chr((ord($b[8]) & 0x3f) | 0x80);
        return vsprintf('%s%s-%s-%s-%s-%s%s%s', str_split(bin2hex($b), 4));
    }
}
