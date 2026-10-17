import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'app/router.dart';
import 'app/theme.dart';

void main() {
  runApp(const ProviderScope(child: ComicMarkApp()));
}

/// The app.
class ComicMarkApp extends ConsumerWidget {
  const ComicMarkApp({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final router = ref.watch(routerProvider);

    return MaterialApp.router(
      title: 'ComicMark',
      debugShowCheckedModeBanner: false,
      theme: ComicMarkTheme.light(),
      darkTheme: ComicMarkTheme.dark(),
      // The platform decides, so a reader whose phone is in dark mode gets a
      // dark app without changing anything here.
      themeMode: ThemeMode.system,
      routerConfig: router,
    );
  }
}
