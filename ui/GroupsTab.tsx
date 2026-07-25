import { useState } from 'react';
import {
  Avatar,
  Badge,
  Button,
  EmptyState,
  IconButton,
  Panel,
  Stack,
  Text,
  ChevronRightIcon,
  PlusIcon,
  UserIcon,
  useLiveQuery,
  type ServiceContextProps,
} from '@holistic/ui';
import type { Group, GroupRole, GroupsResponse } from './types';
import { GroupEditor } from './GroupEditor';

// The "Gruppen" tab: the personal contact groups the user owns or belongs to. Creating a group and
// managing its members is per-user self-service (no privleg right) — every action is gated by the
// caller's role, which the backend returns on each group.
export function GroupsTab({ user, api, ui }: Pick<ServiceContextProps, 'user' | 'api' | 'ui'>) {
  const q = useLiveQuery<GroupsResponse>(() => api.get<GroupsResponse>('groups'), 15000);
  const [editor, setEditor] = useState<{ open: boolean; group?: Group }>({ open: false });
  const groups = q.data?.groups ?? [];

  return (
    <Stack gap={4}>
      <Stack direction="row" align="center" justify="end">
        <Button variant="primary" iconLeft={<PlusIcon className="h-4 w-4" />} onClick={() => setEditor({ open: true })}>
          Neue Gruppe
        </Button>
      </Stack>

      <Panel title="Gruppen" className="overflow-hidden">
        {groups.length === 0 ? (
          <EmptyState
            icon={<UserIcon />}
            title={q.loading ? 'Lädt…' : 'Noch keine Gruppen'}
            description="Erstelle eine Gruppe und füge Kontakte hinzu, die du sehen kannst."
          />
        ) : (
          <Stack gap={0}>
            {groups.map((g) => (
              <GroupRow key={g.id} g={g} onOpen={() => setEditor({ open: true, group: g })} />
            ))}
          </Stack>
        )}
      </Panel>

      <GroupEditor
        open={editor.open}
        group={editor.group}
        user={user}
        api={api}
        ui={ui}
        onClose={() => setEditor({ open: false })}
        onChanged={() => q.refresh()}
      />
    </Stack>
  );
}

function GroupRow({ g, onOpen }: { g: Group; onOpen: () => void }) {
  return (
    <Stack direction="row" align="center" gap={3} className="border-b border-separator px-4 py-2.5 last:border-b-0">
      <Avatar name={g.name} size={36} />
      <Stack gap={0} className="min-w-0 flex-1">
        <Text weight="medium" truncate>
          {g.name}
        </Text>
        <Text variant="footnote" color="secondary">
          {g.memberCount} {g.memberCount === 1 ? 'Mitglied' : 'Mitglieder'}
        </Text>
      </Stack>
      <RoleBadge role={g.role} />
      <IconButton label="Öffnen" size="sm" onClick={onOpen}>
        <ChevronRightIcon className="h-4 w-4" />
      </IconButton>
    </Stack>
  );
}

// RoleBadge renders a group role as a labelled badge, reused by the member list in the editor.
export function RoleBadge({ role }: { role: GroupRole }) {
  if (role === 'owner') return <Badge variant="accent">Eigentümer</Badge>;
  if (role === 'admin') return <Badge variant="warning">Admin</Badge>;
  return <Badge variant="neutral">Mitglied</Badge>;
}
