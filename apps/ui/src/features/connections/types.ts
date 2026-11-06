export type ConnectionStatus = {
  configured: boolean;
};

export type ConnectionsStatus = {
  servicenow: ConnectionStatus;
  assistant: ConnectionStatus;
  cursor: ConnectionStatus;
};

export type ServiceNowInput = {
  clientToken: string;
};

export type AssistantInput = {
  apiKey: string;
  baseUrl?: string;
};

export type CursorInput = {
  apiKey: string;
};

export type ConnectionKind = "servicenow" | "assistant" | "cursor";
