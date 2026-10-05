import 'package:fishcam_control/server_config.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('WEBCAM_HOST becomes an https URL', () {
    expect(configuredServerUrl(url: '', host: 'webcam.example.com'),
        'https://webcam.example.com');
  });

  test('WEBCAM_URL wins over WEBCAM_HOST', () {
    expect(
        configuredServerUrl(
            url: 'http://localhost:8082', host: 'webcam.example.com'),
        'http://localhost:8082');
  });

  test('a trailing slash is dropped, so paths join cleanly', () {
    expect(configuredServerUrl(url: 'http://localhost:8082/', host: ''),
        'http://localhost:8082');
  });

  test('neither set means not configured', () {
    expect(configuredServerUrl(url: '', host: ''), isEmpty);
  });
}
