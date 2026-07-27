import { useEffect, useState } from 'react';
import {
  Avatar,
  Badge,
  Button,
  ContactPicker,
  EmptyState,
  Field,
  IconButton,
  Input,
  Modal,
  Panel,
  Spinner,
  Stack,
  Text,
  PencilIcon,
  PlusIcon,
  TrashIcon,
  UserIcon,
  useLiveQuery,
  useT,
  type ContactOption,
  type ServiceContextProps,
} from '@holistic/ui';
import type { Contact, ContactsResponse, GroupSummary, GroupsResponse } from './types';

type Api = ServiceContextProps['api'];
type Ui = ServiceContextProps['ui'];

// The personal-contact-groups surface. Groups are the entity contax owns for the shared
// ContactPicker and sibling services; here the user curates them. Members are added through the very
// same SDK ContactPicker other services use, fed by contax's own lookup — one access point, reused.
export function GroupsPanel({ api, ui }: { api: Api; ui: Ui }) {
  const t = useT();
  const q = useLiveQuery<GroupsResponse>(() => api.get<GroupsResponse>('groups'), 30000);
  const groups = q.data?.groups ?? [];
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<GroupSummary | undefined>(undefined);

  async function remove(g: GroupSummary) {
    const ok = await ui.confirm({
      title: t('contax.deleteTitle', { name: g.name }),
      description: t('contax.deleteGroupBody'),
      danger: true,
      confirmLabel: t('common.delete'),
    });
    if (!ok) return;
    try {
      await api.del(`groups/${encodeURIComponent(g.id)}`);
      q.refresh();
      ui.toast({ title: t('contax.deleted'), variant: 'success' });
    } catch (e) {
      ui.toast({ title: t('contax.deleteFailed'), description: (e as Error).message, variant: 'error' });
    }
  }

  return (
    <Panel
      title={t('contax.groups')}
      className="overflow-hidden"
      actions={
        <Button variant="secondary" iconLeft={<PlusIcon className="h-4 w-4" />} onClick={() => setCreating(true)}>
          {t('contax.createGroup')}
        </Button>
      }
    >
      {groups.length === 0 ? (
        <EmptyState
          icon={<UserIcon />}
          title={t('contax.noGroupsTitle')}
          description={q.loading ? t('contax.loading') : t('contax.noGroupsBody')}
        />
      ) : (
        <Stack gap={0}>
          {groups.map((g) => (
            <Stack
              key={g.id}
              direction="row"
              align="center"
              gap={3}
              className="border-b border-separator px-4 py-2.5 last:border-b-0"
            >
              <Stack gap={0} className="min-w-0 flex-1">
                <Text weight="medium" truncate>
                  {g.name}
                </Text>
              </Stack>
              <Badge variant="neutral">{t('contax.memberCount', { count: g.memberCount })}</Badge>
              <Stack direction="row" align="center" gap={1}>
                <IconButton label={t('contax.editMembers')} size="sm" onClick={() => setEditing(g)}>
                  <PencilIcon className="h-4 w-4" />
                </IconButton>
                <IconButton label={t('common.delete')} size="sm" onClick={() => remove(g)}>
                  <TrashIcon className="h-4 w-4" />
                </IconButton>
              </Stack>
            </Stack>
          ))}
        </Stack>
      )}

      <CreateGroupModal
        open={creating}
        api={api}
        ui={ui}
        onClose={() => setCreating(false)}
        onCreated={(g) => {
          setCreating(false);
          q.refresh();
          setEditing(g); // jump straight into the new group so members can be added right away
        }}
      />

      <GroupEditor
        open={!!editing}
        group={editing}
        api={api}
        ui={ui}
        onClose={() => setEditing(undefined)}
        onChanged={() => q.refresh()}
      />
    </Panel>
  );
}

function CreateGroupModal({
  open,
  api,
  ui,
  onClose,
  onCreated,
}: {
  open: boolean;
  api: Api;
  ui: Ui;
  onClose: () => void;
  onCreated: (g: GroupSummary) => void;
}) {
  const t = useT();
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) setName('');
  }, [open]);

  async function create() {
    const n = name.trim();
    if (!n) {
      ui.toast({ title: t('contax.groupNameRequired'), variant: 'error' });
      return;
    }
    setBusy(true);
    try {
      const g = await api.post<GroupSummary>('groups', { name: n });
      onCreated(g);
    } catch (e) {
      ui.toast({ title: t('contax.createFailed'), description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onOpenChange={(o) => !o && onClose()}
      title={t('contax.newGroup')}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" loading={busy} onClick={create}>
            {t('common.create')}
          </Button>
        </>
      }
    >
      <Field label={t('contax.name')}>
        <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('contax.groupNamePlaceholder')} />
      </Field>
    </Modal>
  );
}

// GroupEditor manages one group's name and members. Members are added via the shared ContactPicker
// wired to contax's own lookup, and removed from the resolved member list; both keep the group in
// sync with contax as the single source.
function GroupEditor({
  open,
  group,
  api,
  ui,
  onClose,
  onChanged,
}: {
  open: boolean;
  group?: GroupSummary;
  api: Api;
  ui: Ui;
  onClose: () => void;
  onChanged: () => void;
}) {
  const t = useT();
  const [name, setName] = useState('');
  const [members, setMembers] = useState<Contact[]>([]);
  const [loading, setLoading] = useState(false);
  const [staging, setStaging] = useState<ContactOption[]>([]);

  useEffect(() => {
    if (open && group) {
      setName(group.name);
      setStaging([]);
      void loadMembers(group.id);
    }
  }, [open, group?.id]);

  async function loadMembers(id: string) {
    setLoading(true);
    try {
      const res = await api.get<ContactsResponse>(`groups/${encodeURIComponent(id)}/members`);
      setMembers(res.contacts ?? []);
    } catch {
      setMembers([]);
    } finally {
      setLoading(false);
    }
  }

  async function saveName() {
    if (!group) return;
    const n = name.trim();
    if (!n || n === group.name) return;
    try {
      await api.put(`groups/${encodeURIComponent(group.id)}`, { name: n });
      onChanged();
    } catch (e) {
      ui.toast({ title: t('contax.renameFailed'), description: (e as Error).message, variant: 'error' });
    }
  }

  // The member list is contax's directory (contacts only — a group never contains a group).
  async function search(query: string): Promise<ContactOption[]> {
    if (!group) return [];
    try {
      const res = await api.get<ContactsResponse>(`lookup?q=${encodeURIComponent(query)}`);
      const have = new Set(members.map((m) => m.email.toLowerCase()));
      return res.contacts
        .filter((c) => c.email && !have.has(c.email.toLowerCase()))
        .map((c) => ({ email: c.email, displayName: c.displayName, avatarUrl: c.avatarUrl, username: c.username }));
    } catch {
      return [];
    }
  }

  async function addPicked(next: ContactOption[]) {
    if (!group) return;
    setStaging([]); // the picker is add-only; clear its chips immediately
    for (const opt of next) {
      try {
        await api.post(`groups/${encodeURIComponent(group.id)}/members`, {
          username: opt.username ?? '',
          email: opt.email ?? '',
        });
      } catch (e) {
        ui.toast({
          title: t('contax.memberNotAdded', { name: opt.displayName || opt.email || '' }),
          description: (e as Error).message,
          variant: 'error',
        });
      }
    }
    await loadMembers(group.id);
    onChanged();
  }

  async function removeMember(c: Contact) {
    if (!group) return;
    const ref = c.kind === 'internal' ? c.username ?? c.id : c.id;
    try {
      await api.del(`groups/${encodeURIComponent(group.id)}/members/${encodeURIComponent(ref)}`);
      await loadMembers(group.id);
      onChanged();
    } catch (e) {
      ui.toast({ title: t('contax.removeFailed'), description: (e as Error).message, variant: 'error' });
    }
  }

  return (
    <Modal open={open} onOpenChange={(o) => !o && onClose()} title={t('contax.editGroup')} size="lg">
      <Stack gap={4}>
        <Field label={t('contax.name')}>
          <Input value={name} onChange={(e) => setName(e.target.value)} onBlur={saveName} />
        </Field>

        <Field label={t('contax.addMember')}>
          <ContactPicker
            value={staging}
            onChange={addPicked}
            onSearch={search}
            allowFreeText={false}
            placeholder={t('contax.searchContacts')}
          />
        </Field>

        <Stack gap={0}>
          {loading ? (
            <Stack align="center" className="py-6">
              <Spinner className="h-5 w-5" />
            </Stack>
          ) : members.length === 0 ? (
            <EmptyState icon={<UserIcon />} title={t('contax.noMembersTitle')} description={t('contax.noMembersBody')} />
          ) : (
            members.map((c) => (
              <Stack
                key={`${c.kind}:${c.id}`}
                direction="row"
                align="center"
                gap={3}
                className="border-b border-separator py-2 last:border-b-0"
              >
                <Avatar name={c.displayName} src={c.avatarUrl || undefined} size={32} />
                <Stack gap={0} className="min-w-0 flex-1">
                  <Text weight="medium" truncate>
                    {c.displayName}
                  </Text>
                  <Text variant="footnote" color="secondary" truncate>
                    {c.email}
                  </Text>
                </Stack>
                <IconButton label={t('common.remove', { name: c.displayName })} size="sm" onClick={() => removeMember(c)}>
                  <TrashIcon className="h-4 w-4" />
                </IconButton>
              </Stack>
            ))
          )}
        </Stack>
      </Stack>
    </Modal>
  );
}
