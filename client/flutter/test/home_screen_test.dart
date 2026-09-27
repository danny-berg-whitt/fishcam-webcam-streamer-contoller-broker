import 'package:fishcam_control/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';

// These tests stay on the paths that make no network call. The home screen
// builds its WebcamClient against the production URL, so anything that
// reaches the client would go to the test binding's stub HttpClient. The
// request logic itself is covered with a MockClient in
// webcam_client_test.dart.
void main() {
  setUp(() => FlutterSecureStorage.setMockInitialValues({}));

  testWidgets('app boots with no stored token', (tester) async {
    await tester.pumpWidget(const FishCamApp());
    await tester.pumpAndSettle();

    expect(find.text('FishCam'), findsOneWidget);
    expect(find.text('Mute'), findsOneWidget);
    expect(find.text('Unmute'), findsOneWidget);
    expect(find.text('Refresh status'), findsOneWidget);
    expect(find.text('Streaming: —'), findsOneWidget);
    expect(find.text('Muted: —'), findsOneWidget);

    // No token yet, so there is no user to switch away from.
    final logout = tester.widget<IconButton>(
      find.widgetWithIcon(IconButton, Icons.logout),
    );
    expect(logout.onPressed, isNull);

    // No dialog until the person actually asks for something.
    expect(find.text('Enter Access Code'), findsNothing);
  });

  testWidgets('an action with no token prompts, and cancelling is harmless',
      (tester) async {
    await tester.pumpWidget(const FishCamApp());
    await tester.pumpAndSettle();

    await tester.tap(find.text('Mute'));
    await tester.pumpAndSettle();
    expect(find.text('Enter Access Code'), findsOneWidget);

    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();
    expect(find.text('Enter Access Code'), findsNothing);
    expect(find.textContaining('Error'), findsNothing);
    expect(find.text('Muted: —'), findsOneWidget);

    // Cancelling must not have stored anything.
    expect(await const FlutterSecureStorage().readAll(), isEmpty);
  });
}
