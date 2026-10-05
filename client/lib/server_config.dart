/// The server the app talks to, fixed when the app is built:
///
///   WEBCAM_HOST  the public hostname; the app uses https://<host>
///   WEBCAM_URL   a full base URL instead, e.g. http://localhost:8082
///                through `kubectl port-forward`
///
/// Both come from the deployment's settings file:
///
///   flutter build <target> --dart-define-from-file=../deploy.env
const _url = String.fromEnvironment('WEBCAM_URL');
const _host = String.fromEnvironment('WEBCAM_HOST');

/// The configured base URL without a trailing '/', or '' if the build set
/// neither value.
String configuredServerUrl({String url = _url, String host = _host}) {
  final base = url.isNotEmpty ? url : (host.isNotEmpty ? 'https://$host' : '');
  return base.endsWith('/') ? base.substring(0, base.length - 1) : base;
}
