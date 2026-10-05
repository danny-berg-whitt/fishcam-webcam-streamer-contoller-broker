import 'dart:convert';
import 'package:http/http.dart' as http;

/// Client for the FishCam broker's public API. The broker (not this app)
/// holds HMAC_SECRET and signs every request to the Controller; this client
/// only ever presents a per-user bearer token issued out-of-band when the
/// user was provisioned (see README: "Broker: provisioning a user").
class WebcamClient {
  final String baseUrl; // e.g. https://webcam.example.com
  final String prefix; // e.g. /webcam — must match the broker's ROUTE_PREFIX
  final String userToken;
  final http.Client _http;

  WebcamClient({
    required this.baseUrl,
    required this.prefix,
    required this.userToken,
    http.Client? httpClient,
  }) : _http = httpClient ?? http.Client();

  Future<Map<String, dynamic>> _call(String method, String action) async {
    final uri = Uri.parse('$baseUrl$prefix/$action');
    final headers = {'Authorization': 'Bearer $userToken'};

    final response = method == 'GET'
        ? await _http.get(uri, headers: headers)
        : await _http.post(uri, headers: headers);

    if (response.statusCode == 401) {
      throw WebcamAuthException(
          'Token was rejected. It may be wrong or revoked.');
    }
    if (response.statusCode != 200) {
      throw WebcamRequestException(
          'Request failed (${response.statusCode}): ${response.body}');
    }
    return jsonDecode(response.body) as Map<String, dynamic>;
  }

  Future<Map<String, dynamic>> mute() => _call('POST', 'mute');
  Future<Map<String, dynamic>> unmute() => _call('POST', 'unmute');
  Future<Map<String, dynamic>> status() => _call('GET', 'status');

  void close() => _http.close();
}

class WebcamAuthException implements Exception {
  final String message;
  WebcamAuthException(this.message);
  @override
  String toString() => message;
}

class WebcamRequestException implements Exception {
  final String message;
  WebcamRequestException(this.message);
  @override
  String toString() => message;
}
