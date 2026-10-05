import 'package:fishcam_control/token_dialog.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

final _validToken = 'a1' * 32; // 64 lowercase hex characters

/// Opens the dialog and returns its pending result.
Future<Future<String?>> _openDialog(WidgetTester tester) async {
  late Future<String?> result;
  await tester.pumpWidget(MaterialApp(
    home: Builder(
      builder: (context) => Scaffold(
        body: TextButton(
          onPressed: () => result = promptForUserToken(context),
          child: const Text('open'),
        ),
      ),
    ),
  ));
  await tester.tap(find.text('open'));
  await tester.pumpAndSettle();
  expect(find.text('Enter Access Code'), findsOneWidget);
  return result;
}

String _fieldText(WidgetTester tester) =>
    tester.widget<EditableText>(find.byType(EditableText)).controller.text;

void main() {
  testWidgets('a valid token is returned', (tester) async {
    final result = await _openDialog(tester);
    await tester.enterText(find.byType(TextFormField), _validToken);
    await tester.tap(find.text('Continue'));
    await tester.pumpAndSettle();

    expect(find.text('Enter Access Code'), findsNothing);
    expect(await result, _validToken);
  });

  testWidgets('an uppercase paste is accepted and normalised', (tester) async {
    final result = await _openDialog(tester);
    await tester.enterText(find.byType(TextFormField), _validToken.toUpperCase());
    await tester.tap(find.text('Continue'));
    await tester.pumpAndSettle();

    expect(await result, _validToken);
  });

  testWidgets('cancel returns null', (tester) async {
    final result = await _openDialog(tester);
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();

    expect(find.text('Enter Access Code'), findsNothing);
    expect(await result, isNull);
  });

  testWidgets('empty input is rejected and the dialog stays open',
      (tester) async {
    await _openDialog(tester);
    await tester.tap(find.text('Continue'));
    await tester.pump();

    expect(find.text('Access code is required'), findsOneWidget);
    expect(find.text('Enter Access Code'), findsOneWidget);
  });

  testWidgets('a short token is rejected', (tester) async {
    await _openDialog(tester);
    await tester.enterText(find.byType(TextFormField), 'abc123');
    await tester.tap(find.text('Continue'));
    await tester.pump();

    expect(
      find.text('Expected 64 hex characters, as given to you by the admin'),
      findsOneWidget,
    );
  });

  testWidgets('non-hex characters never reach the field', (tester) async {
    await _openDialog(tester);
    await tester.enterText(find.byType(TextFormField), r"12'; $(rm -x)<ab>");
    expect(_fieldText(tester), '12ab');
  });

  testWidgets('input is capped at 64 characters', (tester) async {
    await _openDialog(tester);
    await tester.enterText(find.byType(TextFormField), 'f' * 80);
    expect(_fieldText(tester), hasLength(64));
  });

  testWidgets('the field is obscured and suggestions are off', (tester) async {
    await _openDialog(tester);
    final field = tester.widget<EditableText>(find.byType(EditableText));
    expect(field.obscureText, isTrue);
    expect(field.autocorrect, isFalse);
    expect(field.enableSuggestions, isFalse);
  });
}
