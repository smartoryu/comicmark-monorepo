import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/retry.dart';
import '../../auth/application/auth_controller.dart';
import '../../auth/models/auth_user.dart';

/// The signed-in reader's devices.
///
/// Read fresh each time the screen is opened rather than cached: the list is
/// about what is signed in *now*, and a stale answer would defeat the point of
/// the screen.
final sessionsControllerProvider =
    AsyncNotifierProvider<SessionsController, List<DeviceSession>>(
  SessionsController.new,
  retry: apiRetry,
);

class SessionsController extends AsyncNotifier<List<DeviceSession>> {
  @override
  Future<List<DeviceSession>> build() async {
    return ref.watch(authRepositoryProvider).sessions();
  }

  /// Re-reads the list.
  Future<void> reload() async {
    state = const AsyncLoading();
    state = await AsyncValue.guard(
      () => ref.read(authRepositoryProvider).sessions(),
    );
  }

  /// Signs one device out.
  ///
  /// The row is removed optimistically and put back if the server refuses, so
  /// the list matches what the reader just did.
  Future<void> revoke(String id) async {
    final current = state.value;
    if (current == null) return;

    final index = current.indexWhere((session) => session.id == id);
    if (index < 0) return;

    final removed = current[index];
    state = AsyncData(
      current.where((session) => session.id != id).toList(growable: false),
    );

    try {
      await ref.read(authRepositoryProvider).revokeSession(id);
    } catch (_) {
      final latest = state.value ?? const <DeviceSession>[];
      final restored = [...latest];
      restored.insert(index.clamp(0, restored.length), removed);
      state = AsyncData(restored);
      rethrow;
    }
  }

  /// Signs every other device out.
  Future<void> revokeOthers() async {
    await ref.read(authRepositoryProvider).revokeOtherSessions();
    await reload();
  }

  /// Renames a device.
  Future<void> rename(String id, String label) async {
    final renamed = await ref.read(authRepositoryProvider).renameSession(id, label);

    final current = state.value;
    if (current == null) return;

    final index = current.indexWhere((session) => session.id == id);
    if (index < 0) return;

    final copy = [...current];
    copy[index] = renamed;
    state = AsyncData(copy);
  }
}
