import { unwrap } from "@talanta/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { api } from "../lib/api";
import { describeError, isCode, splitFieldErrors } from "../lib/errors";
import { keys, type Template } from "../lib/queries";
import { Alert, Button, TextField } from "./ui";

interface SaveTemplateFormProps {
  venueId: number;
  /** The event whose categories and painting are saved. Undefined while none is chosen. */
  eventId: number | undefined;
  /** Shown before the name box, for example an event picker. */
  children?: ReactNode;
  disabledReason?: string | null;
}

/** Saves an event's categories, prices and painting under a name, so the next event starts from it. */
export function SaveTemplateForm({ venueId, eventId, children, disabledReason }: SaveTemplateFormProps) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [localError, setLocalError] = useState<string | null>(null);
  const [saved, setSaved] = useState<Template | null>(null);

  const save = useMutation({
    mutationFn: (body: { name: string; event_id: number }) =>
      unwrap(api.POST("/admin/venues/{venueId}/templates", { params: { path: { venueId } }, body })),
    onSuccess: (template) => {
      setSaved(template);
      setName("");
      return queryClient.invalidateQueries({ queryKey: keys.templates(venueId) });
    },
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    if (save.isPending || disabledReason) return;
    const trimmed = name.trim();
    if (!trimmed) return setLocalError("Give the template a name, for example League match.");
    if (eventId === undefined) return setLocalError("Pick the event to copy from first.");
    setLocalError(null);
    setSaved(null);
    save.mutate({ name: trimmed, event_id: eventId });
  }

  const { fields } = splitFieldErrors(save.error, ["name", "event_id"] as const);
  const nameError =
    localError ??
    (isCode(save.error, "ALREADY_EXISTS") ? "This venue already has a template with this name. Pick another name." : (fields.name ?? null));
  const formError =
    save.error && !nameError
      ? (fields.event_id ??
        describeError(save.error, {
          VENUE_MISMATCH: "That event is at a different venue, so it cannot be saved as a template here.",
          NOT_FOUND: "The event or venue no longer exists.",
        }))
      : null;

  return (
    <form className="stack" onSubmit={submit} noValidate>
      {disabledReason && <Alert tone="info">{disabledReason}</Alert>}
      <div className="inline-form">
        {children}
        <TextField
          label="Template name"
          name="template-name"
          maxLength={80}
          placeholder="League match"
          value={name}
          disabled={Boolean(disabledReason)}
          onChange={(e) => {
            setName(e.target.value);
            setLocalError(null);
            if (save.isError) save.reset();
          }}
          error={nameError}
        />
        <Button type="submit" variant="secondary" loading={save.isPending} disabled={Boolean(disabledReason)}>
          Save as template
        </Button>
      </div>
      {formError && <Alert tone="error">{formError}</Alert>}
      {saved && (
        <Alert tone="success" title={`Saved "${saved.name}"`}>
          {saved.categories.length} {saved.categories.length === 1 ? "category" : "categories"}, {saved.block_assignment_count} painted blocks and{" "}
          {saved.row_assignment_count} row overrides. It will be offered when you create the next event.
        </Alert>
      )}
    </form>
  );
}
