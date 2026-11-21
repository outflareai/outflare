/// Outflare for Flutter: screens and events from an app, sent with the
/// app's public site key to POST /v1/site/events. The visitor's id and the
/// events still waiting are kept in files, so they survive the app being
/// closed. It is plain Dart (dart:io), with no plugins.
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';

/// The library's version.
const outflareVersion = '0.110.0';
const _library = 'outflare-flutter';
const _maxBatch = 20;
final _idShape = RegExp(r'^[A-Za-z0-9-]{8,64}$');

/// How a request is sent: replaced in tests. It returns the HTTP status, or
/// 0 for no answer.
typedef OutflareTransport = Future<int> Function(
    Uri url, String userAgent, String body);

/// One app's connection to Outflare. Make one and share it.
class Outflare {
  /// [key] is the app's public site key (ofk_...), from a site key made
  /// with no origins. [directory] is where the visitor id and waiting
  /// events are kept: the path of getApplicationSupportDirectory() fits.
  Outflare({
    required this.key,
    required String directory,
    String host = 'https://app.outflare.ai',
    this.appName,
    this.appVersion,
    this.appBuild,
    this.deviceModel,
    this.deviceManufacturer,
    String? osName,
    String? osVersion,
    int flushAt = _maxBatch,
    this.flushInterval = const Duration(seconds: 30),
    this.maxQueue = 1000,
    this.onError,
    OutflareTransport? transport,
  })  : _url =
            Uri.parse('${host.replaceAll(RegExp(r'/+$'), '')}/v1/site/events'),
        _idFile = File('$directory/outflare-id'),
        _queueFile = File('$directory/outflare-queue.json'),
        _flushAt = flushAt.clamp(1, _maxBatch),
        osName = osName ?? _osName(),
        osVersion = osVersion ?? _osVersion(),
        _transport = transport ?? _post {
    if (key.isEmpty) throw ArgumentError('Outflare: key is required');
    _ready = _load(Directory(directory));
  }

  final String key;
  final String? appName, appVersion, appBuild;
  final String? deviceModel, deviceManufacturer;
  final String osName;
  final String? osVersion;

  /// Send at least this often.
  final Duration flushInterval;

  /// The most events kept waiting; the oldest are dropped past it.
  final int maxQueue;

  /// Called when events are given up on, with how many.
  final void Function(String message, int lost)? onError;

  final Uri _url;
  final File _idFile, _queueFile;
  final int _flushAt;
  final OutflareTransport _transport;
  late final Future<void> _ready;
  String _id = '';
  List<Map<String, Object?>> _queue = [];
  Timer? _timer;
  Future<void> _sending = Future.value();

  /// The visitor's id on this phone. Send it to your server with your
  /// user's id to join the two.
  Future<String> get anonymousId async {
    await _ready;
    return _id;
  }

  /// A screen the person saw.
  void screen(String name, [Map<String, Object?> properties = const {}]) {
    if (name.trim().isEmpty)
      throw ArgumentError('Outflare: screen needs a name');
    _add({'type': 'screen', 'name': name, 'properties': properties});
  }

  /// Something the person did, such as Order Started.
  void track(String event, [Map<String, Object?> properties = const {}]) {
    if (event.trim().isEmpty)
      throw ArgumentError('Outflare: track needs an event name');
    _add({'type': 'track', 'event': event, 'properties': properties});
  }

  /// Forget the visitor: call it when someone signs out, so the next
  /// person starts fresh.
  Future<void> reset() async {
    await flush();
    _id = _uuid();
    await _write(_idFile, _id);
  }

  /// Send everything waiting now. Completes once it has been sent or kept
  /// for later. Call it when the app goes to the background.
  Future<void> flush() {
    _timer?.cancel();
    _timer = null;
    return _sending = _sending.then((_) => _drain());
  }

  /// The User-Agent requests carry: the library and the phone, the way
  /// Outflare reads them.
  String get userAgent {
    final v = osVersion ?? '';
    switch (osName.toLowerCase()) {
      case 'ios':
        return '$_library/$outflareVersion (iPhone; CPU iPhone OS ${v.replaceAll('.', '_')} like Mac OS X)';
      case 'android':
        return '$_library/$outflareVersion (Linux; Android $v; ${deviceModel ?? 'Phone'}) Mobile';
    }
    return '$_library/$outflareVersion';
  }

  Map<String, Object?> get _context {
    Map<String, Object?>? some(Map<String, String?> m) {
      m.removeWhere((_, v) => v == null || v.isEmpty);
      return m.isEmpty ? null : m;
    }

    final c = <String, Object?>{
      'library': {'name': _library, 'version': outflareVersion},
      'app': some({'name': appName, 'version': appVersion, 'build': appBuild}),
      'device': some({
        'manufacturer': deviceManufacturer,
        'model': deviceModel,
        'type': osName.toLowerCase()
      }),
      'os': some({'name': osName, 'version': osVersion}),
      'locale': Platform.localeName.replaceAll('_', '-').split('.').first,
    };
    c.removeWhere((_, v) => v == null);
    return c;
  }

  Future<void> _load(Directory dir) async {
    try {
      await dir.create(recursive: true);
      if (await _idFile.exists()) _id = (await _idFile.readAsString()).trim();
      if (await _queueFile.exists()) {
        final kept = jsonDecode(await _queueFile.readAsString());
        // Events from the last launch go first.
        if (kept is List)
          _queue = [...kept.whereType<Map<String, dynamic>>(), ..._queue];
      }
    } catch (_) {
      // Storage that can't be read is the same as none.
    }
    if (!_idShape.hasMatch(_id)) {
      _id = _uuid();
      await _write(_idFile, _id);
    }
  }

  void _add(Map<String, Object?> event) {
    _queue.add({...event, 'messageId': _uuid(), 'context': _context});
    if (_queue.length > maxQueue) {
      final lost = _queue.length - maxQueue;
      _queue.removeRange(0, lost);
      onError?.call(
          'Outflare: too many events are waiting, so the oldest were dropped',
          lost);
    }
    unawaited(_save());
    if (_queue.length >= _flushAt) {
      unawaited(flush());
    } else {
      _timer ??= Timer(flushInterval, () => unawaited(flush()));
    }
  }

  Future<void> _save() async {
    await _ready;
    try {
      if (_queue.isEmpty) {
        if (await _queueFile.exists()) await _queueFile.delete();
      } else {
        await _queueFile.writeAsString(jsonEncode(_queue));
      }
    } catch (_) {
      // The events are still in memory.
    }
  }

  Future<void> _drain() async {
    await _ready;
    while (_queue.isNotEmpty) {
      final batch = _queue.take(_maxBatch).toList();
      final body = jsonEncode({
        'key': key,
        'batch': [
          for (final e in batch) {...e, 'anonymousId': _id},
        ],
      });
      var status = 0;
      try {
        status = await _transport(_url, userAgent, body);
      } catch (_) {
        status = 0;
      }
      // No answer, or a busy server: keep the events for the next try.
      if (status == 0 || status == 408 || status == 429 || status >= 500) break;
      _queue.removeRange(0, batch.length);
      if (status < 200 || status > 299) {
        onError?.call(
            'Outflare: the server refused the events ($status); check the site key',
            batch.length);
      }
    }
    await _save();
  }

  static Future<void> _write(File f, String text) async {
    try {
      await f.writeAsString(text);
    } catch (_) {
      // The id lasts until the app closes.
    }
  }

  static Future<int> _post(Uri url, String userAgent, String body) async {
    final client = HttpClient()
      ..connectionTimeout = const Duration(seconds: 10);
    try {
      final req = await client.postUrl(url);
      req.headers
          .set(HttpHeaders.contentTypeHeader, 'text/plain;charset=UTF-8');
      req.headers.set(HttpHeaders.userAgentHeader, userAgent);
      req.add(utf8.encode(body));
      final res = await req.close().timeout(const Duration(seconds: 15));
      await res.drain<void>();
      return res.statusCode;
    } finally {
      client.close(force: true);
    }
  }

  static String _osName() {
    if (Platform.isIOS) return 'iOS';
    if (Platform.isAndroid) return 'Android';
    return Platform.operatingSystem;
  }

  // iOS says "Version 18.1 (Build 22B83)". Android gives a build id, not
  // its version, so an app passes osVersion itself there.
  static String? _osVersion() {
    if (!Platform.isIOS) return null;
    return RegExp(r'(\d+(?:\.\d+)*)')
        .firstMatch(Platform.operatingSystemVersion)
        ?.group(1);
  }

  static final _random = Random.secure();

  static String _uuid() {
    final b = List<int>.generate(16, (_) => _random.nextInt(256));
    b[6] = (b[6] & 0x0f) | 0x40;
    b[8] = (b[8] & 0x3f) | 0x80;
    final h = b.map((x) => x.toRadixString(16).padLeft(2, '0')).join();
    return '${h.substring(0, 8)}-${h.substring(8, 12)}-${h.substring(12, 16)}-${h.substring(16, 20)}-${h.substring(20)}';
  }
}
