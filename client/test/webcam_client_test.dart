import 'dart:convert';

import 'package:fishcam_control/webcam_client.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

const _token = 'abc123';
const _okBody =
    '{"streaming":true,"muted":false,"uptime":"4h12m30s","restarts":0}';

void main() {
  late List<http.Request> requests;

  WebcamClient clientReturning(int status, String body) {
    requests = [];
    return WebcamClient(
      baseUrl: 'https://fishcam.example',
      prefix: '/webcam',
      userToken: _token,
      httpClient: MockClient((req) async {
        requests.add(req);
        return http.Response(body, status);
      }),
    );
  }

  group('request shape', () {
    // The broker returns 405 for a method mismatch, so each action must use
    // exactly the method the API table specifies.
    final cases = <String, (Future<Map<String, dynamic>> Function(WebcamClient), String)>{
      'status': ((c) => c.status(), 'GET'),
      'mute': ((c) => c.mute(), 'POST'),
      'unmute': ((c) => c.unmute(), 'POST'),
    };

    for (final MapEntry(key: action, value: (call, method))
        in cases.entries) {
      test('$action uses $method on the prefixed path with a bearer token',
          () async {
        final client = clientReturning(200, _okBody);
        await call(client);

        expect(requests, hasLength(1));
        final req = requests.single;
        expect(req.method, method);
        expect(req.url.toString(), 'https://fishcam.example/webcam/$action');
        expect(req.headers['Authorization'], 'Bearer $_token');
        // The app never signs anything; HMAC is the broker's job.
        expect(req.headers.containsKey('X-Auth-Nonce'), isFalse);
      });
    }
  });

  test('parses the status payload', () async {
    final result = await clientReturning(200, _okBody).status();
    expect(result, jsonDecode(_okBody));
    expect(result['muted'], isFalse);
  });

  test('401 raises WebcamAuthException', () async {
    await expectLater(
      clientReturning(401, 'invalid token').mute(),
      throwsA(isA<WebcamAuthException>()),
    );
  });

  test('other failures raise WebcamRequestException with the status code',
      () async {
    await expectLater(
      clientReturning(502, 'upstream unavailable').status(),
      throwsA(isA<WebcamRequestException>()
          .having((e) => e.message, 'message', contains('502'))),
    );
  });
}
