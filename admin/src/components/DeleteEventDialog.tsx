import { unwrap } from "@talanta/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { describeError, isCode } from "../lib/errors";
import { STATUS_LABELS, type AdminEvent } from "../lib/queries";
import { ConfirmDialog } from "./ui";

interface DeleteEventDialogProps {
  /** The draft to delete; null keeps the dialog closed. */
  event: Pick<AdminEvent, "id" | "title"> | null;
  onClose: () => void;
  /** Called once the event is gone, before the lists are refreshed. */
  onDeleted: (event: Pick<AdminEvent, "id" | "title">) => void;
}

/**
 * Asks before deleting a draft event, then deletes it. Only drafts can be deleted: the
 * buttons that open this are shown for drafts only, and the server refuses anything else.
 */
export function DeleteEventDialog({ event, onClose, onDeleted }: DeleteEventDialogProps) {
  const queryClient = useQueryClient();

  const remove = useMutation({
    mutationFn: async (target: Pick<AdminEvent, "id" | "title">) => {
      try {
        await unwrap(api.DELETE("/admin/events/{eventId}", { params: { path: { eventId: target.id } } }));
      } catch (error) {
        // Already gone (deleted in another tab, or by a colleague): that is what was asked for.
        if (!isCode(error, "NOT_FOUND")) throw error;
      }
      return target;
    },
    onSuccess: (target) => {
      onDeleted(target);
      return queryClient.invalidateQueries({ queryKey: ["events", "list"] });
    },
    onError: (error) => {
      // The event turned out not to be a draft: show its real status behind the dialog.
      if (isCode(error, "EVENT_NOT_EDITABLE")) void queryClient.invalidateQueries({ queryKey: ["events"] });
    },
  });

  function close() {
    remove.reset();
    onClose();
  }

  const error = remove.error
    ? isCode(remove.error, "EVENT_NOT_EDITABLE")
      ? `This event is ${statusWord(remove.error.details)} now, so it can no longer be deleted. To stop it, open it and use Cancel event.`
      : describeError(remove.error)
    : null;

  return (
    <ConfirmDialog
      open={event !== null}
      title="Delete this draft event?"
      confirmLabel={remove.isPending ? "Deleting…" : remove.isError ? "Try again" : "Delete the draft"}
      cancelLabel="Keep it"
      danger
      busy={remove.isPending}
      error={error}
      onConfirm={() => event && remove.mutate(event)}
      onCancel={close}
    >
      <p>
        <strong>{event?.title}</strong> will be deleted, with its categories, prices and seat painting.
      </p>
      <p>
        Nothing was on sale, so no fan is affected. Venue templates saved from it are kept.{" "}
        <strong>This cannot be undone.</strong>
      </p>
    </ConfirmDialog>
  );
}

function statusWord(details: unknown): string {
  const status = (details as { current_status?: keyof typeof STATUS_LABELS } | null)?.current_status;
  return status && STATUS_LABELS[status] ? STATUS_LABELS[status].toLowerCase() : "not a draft";
}
