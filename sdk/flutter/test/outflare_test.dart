import 'dart:convert';
import 'dart:io';

import 'package:outflare/outflare.dart';
import 'package:test/test.dart';

class Fake {
  Fake([List<int>? answers]) : answers = answers ?? [];
  final List<int> answers;
  final calls = <({Uri url, String userAgent, Map<String, dynamic> body})>[];

  Future<int> call(Uri url, String userAgent, String body) async {
    calls.add((
      url: url,
      userAgent: userAgent,
      body: jsonDecode(body) as Map<String, dynamic>
    ));
    final status = answers.isEmpty ? 200 : answers.removeAt(0);
    if (status == 0) throw const SocketException('network down');
    return status;
  }
}

List<dynamic> batch(Fake f, int call) =>
    f.calls[call].body['batch'] as List<dynamic>;

void main() {
  late Directory dir;
  setUp(() => dir = Directory.systemTemp.createTempSync('outflare-test'));
  tearDown(() => dir.deleteSync(recursive: true));

  Outflare make(Fake f, {void Function(String, int)? onError}) => Outflare(
        key: 'ofk_test_not_a_real_key',
        directory: dir.path,
        host: 'https://outflare.example/',
        appName: 'Northpeak',
        appVersion: '2.4.0',
        osName: 'iOS',
        osVersion: '18.1',
        flushInterval: const Duration(minutes: 10),
        transport: f.call,
        onError: onError,
      );

  test(
      'screens and events go to the site route with the key and one visitor id',
      () async {
    final f = Fake();
    final o = make(f);
    o.screen('Checkout');
    o.track('Order Started', {'items': 2});
    expect(f.calls, isEmpty);
    await o.flush();
    expect(f.calls, hasLength(1));
    expect(
        f.calls[0].url.toString(), 'https://outflare.example/v1/site/events');
    expect(f.calls[0].userAgent,
        'outflare-flutter/$outflareVersion (iPhone; CPU iPhone OS 18_1 like Mac OS X)');
    expect(f.calls[0].body['key'], 'ofk_test_not_a_real_key');
    final b = batch(f, 0);
    expect(b.map((e) => (e as Map)['type']), ['screen', 'track']);
    expect(b[0]['name'], 'Checkout');
    expect(b[1]['event'], 'Order Started');
    expect(b[1]['properties'], {'items': 2});
    expect(b[0]['anonymousId'], await o.anonymousId);
    expect(b[0]['messageId'], isNot(b[1]['messageId']));
    expect(b[0]['context']['app'], {'name': 'Northpeak', 'version': '2.4.0'});
    expect(b[0]['context']['library']['name'], 'outflare-flutter');
  });

  test('at most 20 events go in one request', () async {
    final f = Fake();
    final o = make(f);
    for (var i = 0; i < 45; i++) {
      o.track('Tapped');
    }
    await o.flush();
    expect([for (var i = 0; i < f.calls.length; i++) batch(f, i).length],
        [20, 20, 5]);
  });

  test(
      'events wait through no answer and a busy server, and are dropped on a refusal',
      () async {
    final f = Fake([0, 503, 200, 401]);
    final lost = <int>[];
    final o = make(f, onError: (_, n) => lost.add(n));
    o.track('Kept');
    await o.flush();
    await o.flush();
    expect(f.calls, hasLength(2));
    await o.flush();
    expect(f.calls, hasLength(3));
    // The same message id each time, so a retry is stored once.
    expect(batch(f, 0)[0]['messageId'], batch(f, 2)[0]['messageId']);
    await o.flush();
    expect(f.calls, hasLength(3));
    o.track('Refused');
    await o.flush();
    expect(lost, [1]);
    await o.flush();
    expect(f.calls, hasLength(4));
  });

  test(
      'the visitor id and waiting events survive a restart; reset starts a new visitor',
      () async {
    final first = make(Fake([0]));
    final id = await first.anonymousId;
    first.screen('Home');
    await first.flush();
    expect(
        jsonDecode(File('${dir.path}/outflare-queue.json').readAsStringSync()),
        hasLength(1));

    final up = Fake();
    final second = make(up);
    expect(await second.anonymousId, id);
    await second.flush();
    expect(batch(up, 0)[0]['name'], 'Home');
    expect(File('${dir.path}/outflare-queue.json').existsSync(), isFalse);

    await second.reset();
    expect(await second.anonymousId, isNot(id));
    expect(File('${dir.path}/outflare-id').readAsStringSync(),
        await second.anonymousId);
  });
}
