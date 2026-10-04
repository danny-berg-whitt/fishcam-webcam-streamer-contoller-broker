import 'package:flutter/material.dart';

import 'token_dialog.dart';
import 'token_storage.dart';
import 'webcam_client.dart';

const _baseUrl = 'https://fishcam.berg-whitt.com';
const _prefix = '/webcam';

class WebcamHomeScreen extends StatefulWidget {
  const WebcamHomeScreen({super.key});

  @override
  State<WebcamHomeScreen> createState() => _WebcamHomeScreenState();
}

class _WebcamHomeScreenState extends State<WebcamHomeScreen> {
  final _tokenStorage = TokenStorage();

  WebcamClient? _client;
  Map<String, dynamic>? _lastStatus;
  bool _busy = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    _restoreSession();
  }

  Future<void> _restoreSession() async {
    final saved = await _tokenStorage.read();
    if (saved != null) {
      setState(() => _client = WebcamClient(
            baseUrl: _baseUrl,
            prefix: _prefix,
            userToken: saved,
          ));
      await _refreshStatus();
    }
  }

  /// Prompts for the access code if no client is set up yet, persists it,
  /// and returns a ready client — or null if the person cancelled.
  Future<WebcamClient?> _ensureClient() async {
    if (_client != null) return _client;
    final token = await promptForUserToken(context);
    if (token == null) return null;
    await _tokenStorage.write(token);
    final client = WebcamClient(baseUrl: _baseUrl, prefix: _prefix, userToken: token);
    setState(() => _client = client);
    return client;
  }

  Future<void> _switchUser() async {
    await _tokenStorage.clear();
    _client?.close();
    setState(() {
      _client = null;
      _lastStatus = null;
      _error = null;
    });
  }

  Future<void> _run(Future<Map<String, dynamic>> Function(WebcamClient) action) async {
    final client = await _ensureClient();
    if (client == null) return;

    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final result = await action(client);
      setState(() => _lastStatus = result);
    } on WebcamAuthException {
      // The stored token was rejected (wrong or revoked). Drop it rather
      // than keep retrying with a token the broker will never accept.
      await _tokenStorage.clear();
      _client?.close();
      setState(() {
        _client = null;
        _error = 'Access code was rejected. Enter it again.';
      });
    } catch (e) {
      setState(() => _error = e.toString());
    } finally {
      setState(() => _busy = false);
    }
  }

  Future<void> _refreshStatus() => _run((c) => c.status());
  Future<void> _mute() => _run((c) => c.mute());
  Future<void> _unmute() => _run((c) => c.unmute());

  @override
  Widget build(BuildContext context) {
    final streaming = _lastStatus?['streaming'] as bool?;
    final muted = _lastStatus?['muted'] as bool?;
    final uptime = _lastStatus?['uptime'] as String?;

    return Scaffold(
      appBar: AppBar(
        title: const Text('FishCam'),
        actions: [
          IconButton(
            icon: const Icon(Icons.logout),
            tooltip: 'Switch user',
            onPressed: _client == null ? null : _switchUser,
          ),
        ],
      ),
      body: RefreshIndicator(
        onRefresh: _refreshStatus,
        child: ListView(
          padding: const EdgeInsets.all(24),
          children: [
            if (_error != null)
              Card(
                color: Theme.of(context).colorScheme.errorContainer,
                child: Padding(
                  padding: const EdgeInsets.all(16),
                  child: Text(_error!),
                ),
              ),
            const SizedBox(height: 16),
            Card(
              child: Padding(
                padding: const EdgeInsets.all(16),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text('Status', style: Theme.of(context).textTheme.titleMedium),
                    const SizedBox(height: 8),
                    Text('Streaming: ${streaming == null ? '—' : (streaming ? 'yes' : 'no')}'),
                    Text('Muted: ${muted == null ? '—' : (muted ? 'yes' : 'no')}'),
                    Text('Uptime: ${uptime ?? '—'}'),
                  ],
                ),
              ),
            ),
            const SizedBox(height: 24),
            Row(
              children: [
                Expanded(
                  child: ElevatedButton.icon(
                    onPressed: _busy ? null : _mute,
                    icon: const Icon(Icons.mic_off),
                    label: const Text('Mute'),
                  ),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: ElevatedButton.icon(
                    onPressed: _busy ? null : _unmute,
                    icon: const Icon(Icons.mic),
                    label: const Text('Unmute'),
                  ),
                ),
              ],
            ),
            const SizedBox(height: 12),
            OutlinedButton.icon(
              onPressed: _busy ? null : _refreshStatus,
              icon: const Icon(Icons.refresh),
              label: const Text('Refresh status'),
            ),
          ],
        ),
      ),
    );
  }
}
