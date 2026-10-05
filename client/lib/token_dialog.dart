import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

/// Prompts for an access code (from `make broker-init` or `broker-add-user`);
/// null if cancelled. The caller decides whether to store it.
Future<String?> promptForUserToken(BuildContext context) {
  return showDialog<String>(
    context: context,
    barrierDismissible: false,
    builder: (context) => const _UserTokenDialog(),
  );
}

/// Codes are 64 lowercase hex characters (`openssl rand -hex 32`); checking
/// the shape catches paste errors before they become a confusing 401.
final RegExp _hexTokenPattern = RegExp(r'^[0-9a-f]{64}$');

class _UserTokenDialog extends StatefulWidget {
  const _UserTokenDialog();

  @override
  State<_UserTokenDialog> createState() => _UserTokenDialogState();
}

class _UserTokenDialogState extends State<_UserTokenDialog> {
  final _formKey = GlobalKey<FormState>();
  final _controller = TextEditingController();
  bool _obscure = true;

  @override
  void dispose() {
    _controller.text = '';
    _controller.dispose();
    super.dispose();
  }

  String? _validate(String? value) {
    final trimmed = value?.trim().toLowerCase() ?? '';
    if (trimmed.isEmpty) return 'Access code is required';
    if (!_hexTokenPattern.hasMatch(trimmed)) {
      return 'Expected 64 hex characters, as given to you by the admin';
    }
    return null;
  }

  void _submit() {
    if (_formKey.currentState!.validate()) {
      // Tokens are minted lowercase; normalise so an uppercase paste works.
      Navigator.of(context).pop(_controller.text.trim().toLowerCase());
    }
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: const Text('Enter Access Code'),
      content: Form(
        key: _formKey,
        child: TextFormField(
          controller: _controller,
          obscureText: _obscure,
          autocorrect: false,
          enableSuggestions: false,
          enableIMEPersonalizedLearning: false,
          keyboardType: TextInputType.visiblePassword,
          inputFormatters: [
            FilteringTextInputFormatter.allow(RegExp(r'[0-9a-fA-F]')),
            LengthLimitingTextInputFormatter(64),
          ],
          decoration: InputDecoration(
            labelText: 'Access code',
            hintText: 'Given to you when your access was set up',
            suffixIcon: IconButton(
              icon: Icon(_obscure ? Icons.visibility_off : Icons.visibility),
              onPressed: () => setState(() => _obscure = !_obscure),
            ),
          ),
          validator: _validate,
          onFieldSubmitted: (_) => _submit(),
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: const Text('Cancel'),
        ),
        FilledButton(onPressed: _submit, child: const Text('Continue')),
      ],
    );
  }
}
