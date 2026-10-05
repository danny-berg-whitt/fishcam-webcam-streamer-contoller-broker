/// The server address, fixed at build time from deploy.env
/// (`--dart-define-from-file=../deploy.env`): WEBCAM_URL if set, else
/// https://WEBCAM_HOST.
const _url = String.fromEnvironment('WEBCAM_URL');
const _host = String.fromEnvironment('WEBCAM_HOST');

/// Without a trailing '/'; '' if neither is set.
String configuredServerUrl({String url = _url, String host = _host}) {
  final base = url.isNotEmpty ? url : (host.isNotEmpty ? 'https://$host' : '');
  return base.endsWith('/') ? base.substring(0, base.length - 1) : base;
}
