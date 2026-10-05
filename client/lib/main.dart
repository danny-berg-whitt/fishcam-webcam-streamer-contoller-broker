import 'package:flutter/material.dart';

import 'home_screen.dart';
import 'server_config.dart';

void main() {
  runApp(FishCamApp(baseUrl: configuredServerUrl()));
}

class FishCamApp extends StatelessWidget {
  /// The broker's base URL, e.g. https://webcam.example.com; '' if the build
  /// wasn't given one (see server_config.dart).
  final String baseUrl;

  const FishCamApp({super.key, required this.baseUrl});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'FishCam',
      theme: ThemeData(colorSchemeSeed: Colors.teal, useMaterial3: true),
      home: WebcamHomeScreen(baseUrl: baseUrl),
    );
  }
}
