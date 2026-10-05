import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// Keeps the access code in the platform keystore, so it's entered once per
/// device.
class TokenStorage {
  static const _key = 'fishcam_user_token';

  final FlutterSecureStorage _storage;

  TokenStorage({FlutterSecureStorage? storage})
      : _storage = storage ??
            const FlutterSecureStorage(
              aOptions: AndroidOptions(encryptedSharedPreferences: true),
            );

  Future<String?> read() => _storage.read(key: _key);

  Future<void> write(String token) => _storage.write(key: _key, value: token);

  Future<void> clear() => _storage.delete(key: _key);
}
