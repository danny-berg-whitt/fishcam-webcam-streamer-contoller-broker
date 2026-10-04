import 'package:flutter/material.dart';

import 'home_screen.dart';

void main() {
  runApp(const FishCamApp());
}

class FishCamApp extends StatelessWidget {
  const FishCamApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'FishCam',
      theme: ThemeData(colorSchemeSeed: Colors.teal, useMaterial3: true),
      home: const WebcamHomeScreen(),
    );
  }
}
