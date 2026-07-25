import { useEffect, useState } from 'react';
import {
  Avatar,
  Button,
  ContactPicker,
  DropdownMenu,
  Field,
  IconButton,
  Input,
  Modal,
  Stack,
  Text,
  ChevronDownIcon,
  type ContactOption,
  type MenuItem,
  type ServiceContextProps,
} from '@holistic/ui';
import type { Group, GroupMemberView, GroupRole } from './types';
import { RoleBadge } from './GroupsTab';

// Create + manage a personal group. In create mode (no `group`) it shows only a name field; once
// created it flips to manage mode showing the members, with actions gated by the caller's role:
// owner/admin add & remove members and set roles; only the owner deletes or transfers ownership;
// any non-owner member can leave. Members are added from the caller's OWN visible contacts (the
// picker is backed by contax's `lookup`, and the backend re-checks visibility).
export function GroupEditor({
  open,
  group,
  user,
  api,
  ui,
  onClose,
  onChanged,
}: {
  open: boolean;
  group?: Group;
  user: ServiceContextProps['user'];
  api: ServiceContextProps['api'];
  ui: ServiceContextProps['ui'];
  onClose: () => void;
  onChanged: () => void;
}) {
  const [g, setG] = useState<Group | null>(null);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    setG(group ?? null);
    setName(group?.name ?? '');
  }, [open, group]);

  const canManage = !!g && (g.role === 'owner' || g.role === 'admin');
  const isOwner = g?.role === 'owner';

  // run wraps a mutation with the busy flag, a toast, and a parent refresh. Returns the parsed
  // result (or undefined on error) so callers can update the local group from the response.
  async function run<T>(fn: () => Promise<T>, okMsg: string): Promise<T | undefined> {
    setBusy(true);
    try {
      const r = await fn();
      ui.toast({ title: okMsg, variant: 'success' });
      onChanged();
      return r;
    } catch (e) {
      ui.toast({ title: 'Aktion fehlgeschlagen', description: (e as Error).message, variant: 'error' });
      return undefined;
    } finally {
      setBusy(false);
    }
  }

  async function create() {
    const ng = await run(() => api.post<Group>('groups', { name: name.trim() }), 'Gruppe erstellt');
    if (ng) {
      setG(ng);
      setName(ng.name);
    }
  }

  async function rename() {
    if (!g) return;
    const ng = await run(() => api.put<Group>(`groups/${g.id}`, { name: name.trim() }), 'Umbenannt');
    if (ng) setG(ng);
  }

  async function addMember(opt: ContactOption) {
    if (!g) return;
    const body = opt.username
      ? { kind: 'internal', ref: opt.username }
      : { kind: 'external', ref: opt.email };
    const ng = await run(() => api.post<Group>(`groups/${g.id}/members`, body), 'Hinzugefügt');
    if (ng) setG(ng);
  }

  async function setRole(m: GroupMemberView, role: GroupRole) {
    if (!g) return;
    const ng = await run(
      () => api.put<Group>(`groups/${g.id}/members`, { kind: m.kind, ref: m.ref, role }),
      role === 'admin' ? 'Zum Gruppenadmin gemacht' : 'Zum Mitglied gemacht',
    );
    if (ng) setG(ng);
  }

  async function removeMember(m: GroupMemberView) {
    if (!g) return;
    const ok = await ui.confirm({ title: `${m.displayName} entfernen?`, danger: true, confirmLabel: 'Entfernen' });
    if (!ok) return;
    const done = await run(
      () => api.del(`groups/${g.id}/members?kind=${m.kind}&ref=${encodeURIComponent(m.ref)}`),
      'Entfernt',
    );
    if (done) setG({ ...g, members: g.members.filter((x) => !(x.kind === m.kind && x.ref === m.ref)), memberCount: g.memberCount - 1 });
  }

  async function transfer(m: GroupMemberView) {
    if (!g) return;
    const ok = await ui.confirm({
      title: `Eigentum an ${m.displayName} übertragen?`,
      description: 'Du wirst anschließend Admin dieser Gruppe.',
      confirmLabel: 'Übertragen',
    });
    if (!ok) return;
    const ng = await run(() => api.post<Group>(`groups/${g.id}/transfer`, { username: m.ref }), 'Übertragen');
    if (ng) setG(ng);
  }

  async function deleteGroup() {
    if (!g) return;
    const ok = await ui.confirm({ title: `${g.name} löschen?`, danger: true, confirmLabel: 'Löschen' });
    if (!ok) return;
    const done = await run(() => api.del(`groups/${g.id}`), 'Gelöscht');
    if (done) onClose();
  }

  async function leave() {
    if (!g) return;
    const ok = await ui.confirm({ title: `${g.name} verlassen?`, danger: true, confirmLabel: 'Verlassen' });
    if (!ok) return;
    const done = await run(
      () => api.del(`groups/${g.id}/members?kind=internal&ref=${encodeURIComponent(user.username)}`),
      'Verlassen',
    );
    if (done) onClose();
  }

  // Addable contacts: contax's visible-contact typeahead, minus anyone already in the group.
  async function searchAddable(query: string): Promise<ContactOption[]> {
    if (!g) return [];
    try {
      const res = await api.get<{ contacts: ContactOption[] }>(`lookup?q=${encodeURIComponent(query)}`);
      const present = new Set(g.members.map((m) => m.ref.toLowerCase()));
      return (res.contacts ?? []).filter((c) => !present.has((c.username ?? c.email).toLowerCase()));
    } catch {
      return [];
    }
  }

  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
      title={g ? g.name : 'Neue Gruppe'}
      size="lg"
      footer={
        g ? (
          <>
            {isOwner ? (
              <Button variant="destructive" loading={busy} onClick={deleteGroup}>
                Gruppe löschen
              </Button>
            ) : (
              <Button variant="destructive" loading={busy} onClick={leave}>
                Gruppe verlassen
              </Button>
            )}
            <Button variant="ghost" onClick={onClose}>
              Fertig
            </Button>
          </>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              Abbrechen
            </Button>
            <Button variant="primary" loading={busy} disabled={!name.trim()} onClick={create}>
              Erstellen
            </Button>
          </>
        )
      }
    >
      {g ? (
        <Stack gap={4}>
          {canManage && (
            <Field label="Name">
              <Stack direction="row" gap={2}>
                <Input value={name} maxLength={64} onChange={(e) => setName(e.target.value)} className="flex-1" />
                <Button
                  variant="secondary"
                  loading={busy}
                  disabled={!name.trim() || name.trim() === g.name}
                  onClick={rename}
                >
                  Umbenennen
                </Button>
              </Stack>
            </Field>
          )}

          {canManage && (
            <Field label="Mitglied hinzufügen">
              <ContactPicker
                value={[]}
                onChange={(opts) => {
                  const opt = opts[opts.length - 1];
                  if (opt) void addMember(opt);
                }}
                onSearch={searchAddable}
                allowFreeText={false}
                placeholder="Sichtbare Kontakte suchen …"
              />
            </Field>
          )}

          <Stack gap={0}>
            {g.members.map((m) => (
              <MemberRow
                key={`${m.kind}:${m.ref}`}
                m={m}
                canManage={canManage}
                isOwner={isOwner}
                selfUsername={user.username}
                busy={busy}
                onSetRole={setRole}
                onRemove={removeMember}
                onTransfer={transfer}
              />
            ))}
          </Stack>
        </Stack>
      ) : (
        <Field label="Name">
          <Input
            value={name}
            maxLength={64}
            onChange={(e) => setName(e.target.value)}
            placeholder="z. B. Team, Familie"
          />
        </Field>
      )}
    </Modal>
  );
}

function MemberRow({
  m,
  canManage,
  isOwner,
  selfUsername,
  busy,
  onSetRole,
  onRemove,
  onTransfer,
}: {
  m: GroupMemberView;
  canManage: boolean;
  isOwner: boolean;
  selfUsername: string;
  busy: boolean;
  onSetRole: (m: GroupMemberView, role: GroupRole) => void;
  onRemove: (m: GroupMemberView) => void;
  onTransfer: (m: GroupMemberView) => void;
}) {
  const isSelf = m.kind === 'internal' && m.ref === selfUsername;
  // The owner row and your own row are never managed via this menu (leave via the footer instead).
  const showMenu = canManage && m.role !== 'owner' && !isSelf;

  const items: MenuItem[] = [];
  if (showMenu) {
    if (m.kind === 'internal') {
      items.push(
        m.role === 'member'
          ? { id: 'promote', label: 'Zum Gruppenadmin machen', onSelect: () => onSetRole(m, 'admin') }
          : { id: 'demote', label: 'Zum Mitglied machen', onSelect: () => onSetRole(m, 'member') },
      );
      if (isOwner) {
        items.push({ id: 'transfer', label: 'Eigentümer übertragen', separatorBefore: true, onSelect: () => onTransfer(m) });
      }
    }
    items.push({ id: 'remove', label: 'Entfernen', danger: true, separatorBefore: m.kind === 'internal', onSelect: () => onRemove(m) });
  }

  return (
    <Stack direction="row" align="center" gap={3} className="border-b border-separator py-2 last:border-b-0">
      <Avatar name={m.displayName} src={m.avatarUrl || undefined} size={32} />
      <Stack gap={0} className="min-w-0 flex-1">
        <Text weight="medium" truncate>
          {m.displayName}
        </Text>
        <Text variant="footnote" color="secondary" truncate>
          {m.email}
        </Text>
      </Stack>
      <RoleBadge role={m.role} />
      {showMenu && (
        <DropdownMenu
          trigger={
            <IconButton label="Mitglied verwalten" size="sm" disabled={busy}>
              <ChevronDownIcon className="h-4 w-4" />
            </IconButton>
          }
          items={items}
        />
      )}
    </Stack>
  );
}
